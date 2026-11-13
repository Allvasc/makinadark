package services

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"makinadark/backend/internal/config"
	"makinadark/backend/internal/db"
	"makinadark/backend/internal/models"
)

type GeminiRequest struct {
	Contents         []GeminiContent  `json:"contents"`
	GenerationConfig *GeminiGenConfig `json:"generationConfig,omitempty"`
}

type GeminiContent struct {
	Parts []GeminiPart `json:"parts"`
}

type GeminiPart struct {
	Text string `json:"text"`
}

type GeminiGenConfig struct {
	ResponseMimeType string `json:"responseMimeType"`
}

type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

type GeminiImageRequest struct {
	Contents         []GeminiContent      `json:"contents"`
	GenerationConfig GeminiImageGenConfig `json:"generationConfig"`
}

type GeminiImageGenConfig struct {
	ResponseModalities []string        `json:"responseModalities"`
	ImageConfig        GeminiImageOpts `json:"imageConfig"`
}

type GeminiImageOpts struct {
	AspectRatio string `json:"aspectRatio"`
	ImageSize   string `json:"imageSize"`
}

type GeminiImageResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

type OpenAIRequest struct {
	Model          string          `json:"model"`
	Messages       []OpenAIMessage `json:"messages"`
	ResponseFormat *OpenAIFormat   `json:"response_format,omitempty"`
}

type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIFormat struct {
	Type string `json:"type"`
}

type OpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type ClaudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []ClaudeMessage `json:"messages"`
	// Fallbacks "default" (beta server-side-fallback-2026-07-01) lets the API
	// retry a safety-classifier refusal on a suitable fallback model itself.
	Fallbacks string `json:"fallbacks,omitempty"`
}

type ClaudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ClaudeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// llmHTTPTimeout covers full non-streaming script generation (35-55 scenes,
// ~3000-4500 words) across every LLM provider so slow responses don't get cut off
// while awaiting headers.
const llmHTTPTimeout = 5 * time.Minute

// CallLLM sends a prompt to Groq, Gemini, Claude, or OpenAI, requesting a JSON response.
// channelID/videoID attribute the call's cost to the cost dashboard - videoID
// may be empty for channel-level calls (branding, trends) not tied to one video.
func CallLLM(channelID, videoID, prompt string) (string, error) {
	return callLLM(channelID, videoID, prompt, true)
}

// CallLLMText sends a prompt to the configured LLM chain without forcing JSON
// output mode, for callers that expect plain text (e.g. topic selection).
func CallLLMText(channelID, videoID, prompt string) (string, error) {
	return callLLM(channelID, videoID, prompt, false)
}

func callLLM(channelID, videoID, prompt string, jsonMode bool) (string, error) {
	groqKey := config.AppConfig.GroqAPIKey
	groqConfigured := config.IsSet(groqKey)

	claudeKey := config.AppConfig.AnthropicAPIKey
	claudeConfigured := config.IsSet(claudeKey)

	openAIKey := config.AppConfig.OpenAIAPIKey
	openAIConfigured := config.IsSet(openAIKey)

	geminiKey := config.AppConfig.GeminiAPIKey
	geminiConfigured := config.IsSet(geminiKey)

	// Every provider's own failure reason is collected (not just logged) so
	// that if all of them fail, the caller - and ultimately the video's
	// error_message shown in the UI - gets the real reason from each one
	// (invalid key, quota exceeded, timeout, etc.) instead of a generic
	// "all providers failed" that requires digging through server logs to
	// explain. Previously, when OpenAI was configured as the last resort and
	// also failed, only OpenAI's raw error was returned, silently discarding
	// what Groq/Gemini/Claude had actually failed with.
	var failures []string

	if groqConfigured {
		result, err := callGroq(channelID, videoID, prompt, groqKey, jsonMode)
		if err == nil {
			return result, nil
		}
		log.Printf("Groq call failed: %v", err)
		failures = append(failures, "Groq: "+err.Error())
	}

	if geminiConfigured {
		result, err := callGemini(channelID, videoID, prompt, geminiKey, jsonMode)
		if err == nil {
			return result, nil
		}
		log.Printf("Gemini call failed: %v", err)
		failures = append(failures, "Gemini: "+err.Error())
	}

	if claudeConfigured {
		result, err := callClaude(channelID, videoID, prompt, claudeKey, jsonMode)
		if err == nil {
			return result, nil
		}
		log.Printf("Claude call failed: %v", err)
		failures = append(failures, "Claude: "+err.Error())
	}

	if openAIConfigured {
		result, err := callOpenAI(channelID, videoID, prompt, openAIKey, jsonMode)
		if err == nil {
			return result, nil
		}
		log.Printf("OpenAI call failed: %v", err)
		failures = append(failures, "OpenAI: "+err.Error())
	}

	if len(failures) == 0 {
		log.Println("ERROR: No active LLM API keys configured.")
		return "", fmt.Errorf("nenhuma chave de API de LLM configurada (Groq/Gemini/Claude/OpenAI)")
	}

	log.Printf("ERROR: All LLM providers failed: %s", strings.Join(failures, " | "))
	return "", fmt.Errorf("todos os provedores de LLM falharam - %s", strings.Join(failures, " | "))
}

func buildOpenAIChatRequest(model, prompt string, jsonMode bool) OpenAIRequest {
	messages := []OpenAIMessage{{Role: "user", Content: prompt}}
	var respFmt *OpenAIFormat
	if jsonMode {
		respFmt = &OpenAIFormat{Type: "json_object"}
		// OpenAI rejects response_format=json_object with HTTP 400 unless
		// "json" appears somewhere in messages.
		if !strings.Contains(strings.ToLower(prompt), "json") {
			messages = append([]OpenAIMessage{{Role: "system", Content: "Respond with valid JSON."}}, messages...)
		}
	}
	return OpenAIRequest{
		Model:          model,
		Messages:       messages,
		ResponseFormat: respFmt,
	}
}

func callOpenAICompatible(providerName, url, model, apiKey, prompt string, jsonMode bool, timeout time.Duration, recordUsage func(promptTokens, completionTokens int)) (string, error) {
	reqPayload := buildOpenAIChatRequest(model, prompt, jsonMode)

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s api returned status %d: %s", providerName, resp.StatusCode, string(respBody))
	}

	var parsed OpenAIResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", err
	}

	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("%s api returned empty choices", providerName)
	}

	if recordUsage != nil {
		recordUsage(parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens)
	}

	return parsed.Choices[0].Message.Content, nil
}

func callGroq(channelID, videoID, prompt string, apiKey string, jsonMode bool) (string, error) {
	return callOpenAICompatible(
		"groq",
		"https://api.groq.com/openai/v1/chat/completions",
		"llama-3.3-70b-versatile",
		apiKey,
		prompt,
		jsonMode,
		llmHTTPTimeout,
		func(pt, ct int) { RecordGroqTokens(channelID, videoID, pt, ct) },
	)
}

func callClaude(channelID, videoID, prompt string, apiKey string, jsonMode bool) (string, error) {
	url := "https://api.anthropic.com/v1/messages"

	content := prompt
	if jsonMode {
		content += "\n\nIMPORTANTE: Retorne ESTRITAMENTE um objeto JSON válido."
	}

	reqPayload := ClaudeRequest{
		// claude-3-5-sonnet-20241022 was retired on 2025-10-28 - every call
		// to it 404'd, so this provider had been silently dead in the chain.
		Model: claudeModel,
		// Current models think adaptively before answering and that thinking
		// counts against max_tokens, so a full 35-55 scene script needs real
		// headroom to avoid the JSON being cut off mid-object.
		MaxTokens: 16000,
		Messages: []ClaudeMessage{
			{Role: "user", Content: content},
		},
		Fallbacks: "default",
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "server-side-fallback-2026-07-01")

	client := &http.Client{Timeout: llmHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic claude api returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var claudeResp ClaudeResponse
	if err := json.Unmarshal(respBody, &claudeResp); err != nil {
		return "", err
	}

	RecordClaudeTokens(channelID, videoID, claudeResp.Usage.InputTokens, claudeResp.Usage.OutputTokens)

	switch claudeResp.StopReason {
	case "refusal":
		return "", fmt.Errorf("claude recusou a solicitação (stop_reason=refusal)")
	case "max_tokens":
		return "", fmt.Errorf("resposta do claude truncada (max_tokens) - JSON incompleto")
	}

	// Content[0] is a thinking block on current models, not the answer -
	// only text blocks carry the response.
	var sb strings.Builder
	for _, block := range claudeResp.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", fmt.Errorf("anthropic claude api returned no text content")
	}
	return text, nil
}

// claudeModel is the current Sonnet, the direct successor of the retired
// claude-3-5-sonnet this code was written against.
const claudeModel = "claude-sonnet-5-5"

func callGemini(channelID, videoID, prompt string, apiKey string, jsonMode bool) (string, error) {
	// "gemini-2.5-flash" was retired for new accounts (404 "no longer
	// available to new users") - gemini-flash-latest tracks whatever Google
	// currently points free-tier traffic at, so this doesn't need to be
	// manually bumped every time Google rotates the recommended model.
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-flash-latest:generateContent?key=%s", apiKey)

	reqPayload := GeminiRequest{
		Contents: []GeminiContent{
			{
				Parts: []GeminiPart{
					{Text: prompt},
				},
			},
		},
	}
	if jsonMode {
		reqPayload.GenerationConfig = &GeminiGenConfig{
			ResponseMimeType: "application/json",
		}
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return "", err
	}

	// Recorded on attempt (not just success) since the free-tier daily quota
	// (1500 requests/day on the free-tier flash models, per Google's current
	// published limits) is consumed even by calls that fail.
	RecordGeminiRequest(channelID, videoID)

	client := &http.Client{Timeout: llmHTTPTimeout}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini api returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var geminiResp GeminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return "", err
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini api returned empty content")
	}

	return geminiResp.Candidates[0].Content.Parts[0].Text, nil
}

func callOpenAI(channelID, videoID, prompt string, apiKey string, jsonMode bool) (string, error) {
	return callOpenAICompatible(
		"openai",
		"https://api.openai.com/v1/chat/completions",
		"gpt-4o-mini",
		apiKey,
		prompt,
		jsonMode,
		llmHTTPTimeout,
		func(pt, ct int) { RecordOpenAITokens(channelID, videoID, pt, ct) },
	)
}

// GenerateBrandingResult matches the expected JSON structure from LLM
type GenerateBrandingResult struct {
	Name          string `json:"name"`
	Concept       string `json:"concept"`
	Persona       string `json:"persona"`
	VoiceType     string `json:"voice_type"`
	NarrationTone string `json:"narration_tone"`
	LogoPrompt    string `json:"logo_prompt"`
	BannerPrompt  string `json:"banner_prompt"`
}

func GenerateBranding(channelID, niche string) (*GenerateBrandingResult, error) {
	prompt := fmt.Sprintf(`Crie a identidade completa de um canal de conteúdo automatizado (sem apresentador) de altíssimo sucesso no nicho '%s'.
ATENÇÃO: A estética e o tom devem corresponder PERFEITAMENTE ao nicho '%s' (ex: se for finanças, use estética elegante e moderna; se for saúde, use vitalidade e natureza; se for ciência, use futurismo e tecnologia). NUNCA force temas sombrios a menos que o próprio nicho seja sobre mistérios ou terror.

Crie:
1. Nome criativo, profissional e extremamente memorável para o canal.
2. Conceito detalhado explicando a proposta de valor e o estilo do público-alvo.
3. Persona do Narrador adequada ao nicho (ex: "Especialista em Alta Performance e Mente Financeira", ou "Guia de Ciência e Inovação").
4. Tipo de Voz Ideal do Narrador (ex: "Onyx (Voz Masculina Firme e Envolvente)", "Echo (Voz Clara e Inspiradora)" ou "Nova (Voz Feminina Elegante e Cativante)").
5. Tom de Narração (ex: "Entusiasta, Didático, Cativante e com Ritmo Dinâmico").
6. Prompt em inglês para logotipo no DALL-E 3 (ícone moderno centralizado, vetor profissional de alta qualidade, 4k, sem texto).
7. Prompt em inglês para banner horizontal no DALL-E 3 (iluminação cinematográfica alinhada ao nicho, estética visual impressionante, 4k, sem texto).

Retorne a resposta estritamente em formato JSON com estes exatos campos:
{
  "name": "Nome do Canal",
  "concept": "Conceito estruturado e profissional do canal",
  "persona": "Persona narrativa do canal",
  "voice_type": "Tipo de voz ideal",
  "narration_tone": "Tom de narração dinâmico e envolvente",
  "logo_prompt": "Prompt em inglês para logotipo no DALL-E 3",
  "banner_prompt": "Prompt em inglês para banner no DALL-E 3"
}`, niche, niche)

	jsonStr, err := CallLLM(channelID, "", prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to generate branding via LLM: %w", err)
	}

	var result GenerateBrandingResult
	if err := json.Unmarshal([]byte(stripJSONFences(jsonStr)), &result); err != nil {
		return nil, fmt.Errorf("failed to parse branding response: %v", err)
	}

	return &result, nil
}

// stripJSONFences removes ```json / ``` markdown code fences and any stray
// prose before/after the JSON object that some LLM providers add despite
// being told to return raw JSON, by slicing from the first '{' to the last
// '}'. Applied before every json.Unmarshal of an LLM response in this file.
func stripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		s = s[start : end+1]
	}
	return s
}

type SceneOutput struct {
	Index         int    `json:"index"`
	NarrationText string `json:"narration_text"`
	ImagePrompt   string `json:"image_prompt"`
}

type VideoScriptResult struct {
	Title            string        `json:"title"`
	Description      string        `json:"description"`
	Tags             string        `json:"tags"`
	Script           string        `json:"script"`
	ViralScore       int           `json:"viral_score"`
	HookPowerScore   int           `json:"hook_power_score"`
	RetentionScore   int           `json:"retention_score"`
	CTRScore         int           `json:"ctr_score"`
	OriginalityScore int           `json:"originality_score"`
	ScoreNotes       string        `json:"score_notes"`
	Scenes           []SceneOutput `json:"scenes"`
}

func channelHasConfiguredOutro(channelID string) bool {
	if db.DB == nil || strings.TrimSpace(channelID) == "" {
		return false
	}
	var ch models.Channel
	if err := db.DB.Select("outro_video_path", "outro_status").First(&ch, "id = ?", channelID).Error; err != nil {
		return false
	}
	return strings.TrimSpace(ch.OutroVideoPath) != "" || ch.OutroStatus == "ready" || ch.OutroStatus == "generating"
}

func buildVideoScriptPrompt(channelName, niche, topic string, hasOutro bool) string {
	closingRule := "- ÚLTIMA CENA (CALL TO ACTION NATURAL): chamada fluida para engajamento (comentário provocativo sobre a história real e inscrição)."
	if hasOutro {
		closingRule = "- ÚLTIMA CENA (DESFECHO FACTUAL — SEM CTA): este canal JÁ possui um vídeo de encerramento (outro) separado que será exibido logo após a última cena. Portanto, É ESTRITAMENTE PROIBIDO incluir despedida (\"até o próximo vídeo\", \"obrigado por assistir\") ou pedir para seguir, se inscrever, curtir, dar like, ativar o sininho ou comentar na última cena (ou em qualquer outra cena). Encerre o roteiro exclusivamente com o desfecho factual e o impacto final da história, evitando duplo encerramento."
	}

	return fmt.Sprintf(`Você é um roteirista e estrategista sênior especialista em canais Dark no YouTube (estratégia 2026 de alta retenção e monetização).
Crie um ROTEIRO COMPLETO, PROFUNDO E IMPRESSIONANTE para o canal '%s' (Nicho: '%s') sobre o tema: '%s'.

DIRETRIZES ESSENCIAIS DO ALGORITMO YOUTUBE 2026 (FATOS REAIS & ALTA RETENÇÃO):
1. APENAS HISTÓRIAS E FATOS REAIS (ZERO FILOSOFIA/EXISTENCIALISMO): É ESTRITAMENTE PROIBIDO criar roteiros com reflexões filosóficas poéticas, ponderações existenciais abstratas ou divagações genéricas. O roteiro DEVE ser baseado em HISTÓRIAS REAIS, FATOS CONCRETOS, EVENTOS HISTÓRICOS COMPROVADOS, CASOS REAIS DOCUMENTADOS E DESCOBERTAS VERÍDICAS DO MUNDO REAL.
2. ZERO AI SLOP (ORIGINALIDADE & MONETIZAÇÃO): Evite textos robóticos, genéricos ou com linguagem corporativa fria. O roteiro DEVE ter storytelling factual marcante, narrativa baseada em eventos reais, dados/fatos surpreendentes e revelações concretas. Isso garante aprovação 100%% na monetização do YouTube.
3. RETENÇÃO CRÍTICA NOS PRIMEIROS 2 MINUTOS (HOOK & LOOP):
   - CENA 1 (HOOK DE 0-15s): Comece imediatamente sem saudações genéricas ("Bem-vindos ao canal"). Entregue direto a história real impactante ou a revelação do fato concreto.
   - CENA 2 (LOOP DE CURIOSIDADE 15-60s): Estabeleça o elemento inacreditável da história real que só será detalhado mais à frente, mantendo o Average View Duration (AVD) no topo.
4. DURAÇÃO TOTAL E ESTRUTURA DO VÍDEO (RETENÇÃO DE LONGA DURAÇÃO): O roteiro completo DEVE somar entre 3000 e 4500 palavras de narração no total (aproximadamente 20 a 30 minutos de vídeo, considerando ritmo médio de narração de 150 palavras por minuto), distribuídas em 35 a 55 CENAS:
   - CENA 1 = hook (ver item 3), CENA 2 = loop de curiosidade (ver item 3).
   - SE O TEMA ENVOLVER MAIS DE UM CASO/EVENTO/MISTÉRIO (ex: uma lista de mistérios ou casos): desenvolva cada caso A FUNDO seguindo, nesta ordem: (a) o contexto concreto do caso - data, local, nomes das pessoas envolvidas, números relevantes; (b) a explicação oficial/óbvia e especificamente por que ela não convence ou não fecha; (c) a investigação em si - o que foi encontrado, descartado, ou permanece sem resposta, com detalhes verídicos do processo; (d) pelo menos duas teorias concorrentes específicas, com quem as defende e o raciocínio por trás de cada uma; (e) o estado atual do caso e por que ele ainda fascina/assombra as pessoas.
   - QUANTOS CASOS ESCOLHER: isso depende de quantos fatos REAIS e DISTINTOS existem sobre o tema para preencher 35 a 55 cenas sem repetição. Se 3 casos não têm fatos suficientes para preencher o roteiro sem repetir o que já foi dito, ADICIONE MAIS CASOS em vez de esticar os mesmos 3 com cenas de recapitulação. NUNCA decida quantos casos usar antes de saber quanto material real cada um tem.
   - PENÚLTIMA CENA (CLÍMAX & REVELAÇÃO FACTUAL): a grande virada de chave ou desfecho marcante do caso mais forte do vídeo.
   %s
5. DURAÇÃO DE CADA CENA: 70 a 100 palavras de narração fluida, natural, expressiva e rica em detalhes reais.
6. PROFUNDIDADE E ESPECIFICIDADE (PROIBIDO SUPERFICIALIDADE): é ESTRITAMENTE PROIBIDO apenas anunciar um fato e seguir adiante (ex: "as investigações não encontraram respostas conclusivas" sem dizer O QUE foi investigado, POR QUEM, e o que foi encontrado ou descartado no caminho). Toda cena de desenvolvimento deve trazer pelo menos um destes elementos concretos: uma data/número específico, um nome de pessoa/lugar/organização envolvida, um detalhe sensorial ou narrativo do momento do evento, uma declaração ou citação atribuída a alguém, ou uma teoria/especulação nomeada com sua justificativa. Escreva como um jornalista investigativo contando uma história: cada cena responde uma pergunta concreta e levanta a próxima - nunca resuma um fato inteiro numa única frase genérica e passe para o próximo assunto.
7. PROIBIDO REPETIR OU RECAPITULAR (ANTI-ENCHIMENTO): é ESTRITAMENTE PROIBIDO reafirmar em outras palavras um fato que uma cena anterior já contou (ex: contar que Fulano foi excomungado por algo na cena 4 e depois "relembrar" o mesmo fato quase com as mesmas palavras nas cenas 16, 17, 18 e 19 - isso é enchimento, não é permitido). Cada cena precisa acrescentar uma informação NOVA que nenhuma cena anterior mencionou. NÃO escreva frases de preenchimento genéricas tipo "vamos explorar mais a fundo", "essa história é fascinante" ou "ainda há muito a descobrir" sem vir acompanhadas de um fato novo concreto na mesma cena. Se ao revisar o roteiro antes de responder você perceber que uma cena só repete o que já foi dito, SUBSTITUA essa cena por um fato novo e real sobre o tema, ou por um caso adicional (ver item 4).
8. NÃO ESCREVA TRANSIÇÕES ENTRE CENAS OU CASOS: isso é responsabilidade de outro agente (o Diretor de Cinema) na etapa seguinte da produção - escrever sua própria transição aqui vai colidir com a dele. Cada narration_text deve conter só o conteúdo daquela cena, começando direto no fato, sem frases de conexão como "mas", "enquanto isso", "além disso" no início.
9. PROMPTS VISUAIS (image_prompt): Em inglês, ultra detalhados, iluminação cinematográfica 4k, composição de alto contraste adequada ao nicho '%s'. NUNCA insira texto na imagem e NUNCA force estética sombria a menos que o tema seja terror/mistério real.
10. AVALIAÇÃO DE VIRALIDADE (MÉTRICAS PLAYSQUAD 0 a 100): Faça a análise completa e atribua notas técnicas de 0 a 100 para: viral_score, hook_power_score, retention_score, ctr_score, originality_score e inclua uma breve nota explicativa (score_notes).

Retorne estritamente em formato JSON com esta exata estrutura:
{
  "title": "Título Magnético de Alto Clique (CTR)",
  "description": "Descrição SEO otimizada com resumo instigante e hashtags do nicho",
  "tags": "tag1, tag2, tag3, tag4, tag5",
  "script": "Texto completo do roteiro corrido...",
  "viral_score": 92,
  "hook_power_score": 95,
  "retention_score": 90,
  "ctr_score": 94,
  "originality_score": 98,
  "score_notes": "Análise rápida destacando que o hook dos primeiros 15s tem altíssimo impacto e que a retenção estimada aos 2:00 é superior a 70%%.",
  "scenes": [
    {
      "index": 1,
      "narration_text": "Texto completo de narração envolvente da cena 1 (70-100 palavras)...",
      "image_prompt": "Cinematic 4k photograph of [detailed visual scene matching niche], professional lighting, ultra high quality, 8k resolution, photorealistic, no text"
    }
  ]
}

IMPORTANTE: o array "scenes" deve conter TODAS as 35 a 55 cenas do roteiro completo (não resuma nem interrompa antes do fim).`, channelName, niche, topic, closingRule, niche)
}

func scriptSupervisorRubric(hasOutro bool) string {
	rubric := "O roteiro deve ser baseado em histórias/fatos REAIS (nunca filosofia ou ficção genérica), a Cena 1 deve ser um gancho direto sem saudações, o título deve ser magnético mas fiel ao conteúdo, e o roteiro deve ter pelo menos 25 cenas. CRÍTICO: as cenas de desenvolvimento (amostra em development_samples) NÃO podem ser superficiais - reprove se alguma delas apenas anunciar um fato genérico sem data, nome, número, detalhe narrativo ou teoria específica (ex: 'as investigações não encontraram respostas' sozinho, sem dizer o que foi investigado, é raso e deve ser reprovado)."
	if hasOutro {
		rubric += " CRÍTICO: este canal já possui um vídeo de encerramento separado - reprove se last_scene tiver despedida ('até o próximo vídeo', 'obrigado por assistir') ou pedir para o espectador seguir, se inscrever, curtir, dar like, ativar o sininho ou comentar."
	}
	return rubric
}

func GenerateVideoScript(channelID, videoID, channelName, niche, topic string) (*VideoScriptResult, error) {
	hasOutro := channelHasConfiguredOutro(channelID)
	prompt := buildVideoScriptPrompt(channelName, niche, topic, hasOutro)

	// A single malformed/truncated response used to kill the whole video with
	// no recourse - and the 35-55 scene / 3000-4500 word target makes
	// truncation meaningfully more likely than the old 8-12 scene prompt did.
	// Retries also cover a supervisor (Roteirista's own quality gate)
	// rejecting the content itself, not just malformed JSON.
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		p := prompt
		if attempt > 1 && lastErr != nil {
			p += "\n\nATENÇÃO: a resposta anterior teve um problema que precisa ser corrigido: " + lastErr.Error() +
				"\nResponda ESTRITAMENTE com o objeto JSON completo e válido, sem comentários, sem cercas de markdown, sem texto antes ou depois, e com o array \"scenes\" completo do início ao fim (35 a 55 cenas)."
		}

		jsonStr, err := CallLLM(channelID, videoID, p)
		if err != nil {
			lastErr = fmt.Errorf("failed to generate video script via LLM: %w", err)
			continue
		}

		result, err := parseVideoScriptResult(jsonStr)
		if err != nil {
			lastErr = err
			log.Printf("GenerateVideoScript attempt %d/%d failed: %v", attempt, maxAttempts, err)
			continue
		}

		if attempt < maxAttempts {
			// Sample a few development scenes (not just the hook) so the
			// supervisor can actually judge depth/specificity, not only
			// structure - a script can pass every structural check and still
			// be a shallow "stated the fact and moved on" list.
			sampleIdx := []int{2, len(result.Scenes) / 3, (2 * len(result.Scenes)) / 3}
			var samples strings.Builder
			for _, idx := range sampleIdx {
				if idx >= 0 && idx < len(result.Scenes) {
					fmt.Fprintf(&samples, "%q,", result.Scenes[idx].NarrationText)
				}
			}

			verdict := runSupervisorCheck(channelID, videoID, "roteiro (Roteirista)",
				scriptSupervisorRubric(hasOutro),
				fmt.Sprintf(`{"title":%q,"description":%q,"scene_count":%d,"scene_1":%q,"scene_2":%q,"last_scene":%q,"development_samples":[%s]}`,
					result.Title, result.Description, len(result.Scenes), result.Scenes[0].NarrationText, result.Scenes[1].NarrationText, result.Scenes[len(result.Scenes)-1].NarrationText, strings.TrimSuffix(samples.String(), ",")))
			if !verdict.Approved {
				log.Printf("GenerateVideoScript attempt %d/%d: supervisor rejected script: %s", attempt, maxAttempts, verdict.Feedback)
				lastErr = fmt.Errorf("supervisor rejeitou o roteiro: %s", verdict.Feedback)
				continue
			}
		}

		return result, nil
	}

	return nil, lastErr
}

// parseVideoScriptResult strips markdown fences, parses the JSON, and
// validates the result is actually usable. A response that parses as valid
// JSON but was truncated mid-array (or came back with empty narration) is
// just as unusable as a hard parse error, so it's rejected here too instead
// of silently producing a hollow/short video.
func parseVideoScriptResult(raw string) (*VideoScriptResult, error) {
	var result VideoScriptResult
	if err := json.Unmarshal([]byte(stripJSONFences(raw)), &result); err != nil {
		return nil, fmt.Errorf("failed to parse script response: %w", err)
	}

	if strings.TrimSpace(result.Title) == "" {
		return nil, fmt.Errorf("script response missing title")
	}

	const minScenes = 10 // well below the 35-55 target, but enough to reject an obviously truncated response
	if len(result.Scenes) < minScenes {
		return nil, fmt.Errorf("script response has only %d scene(s), expected at least %d - likely truncated", len(result.Scenes), minScenes)
	}
	for i, s := range result.Scenes {
		if strings.TrimSpace(s.NarrationText) == "" {
			return nil, fmt.Errorf("scene %d has empty narration_text", s.Index)
		}
		// The LLM occasionally mislabels a scene's "index" (e.g. duplicates or
		// skips a number) while still emitting the array in the right
		// narrative order - anything downstream that keys off Index (scene
		// creation, the Diretor/Revisor/Sonoplastia agents, and the frontend's
		// display order) would then show that scene out of sequence. The
		// array position is what actually reflects narrative order here, so
		// re-number to match it instead of trusting the model's own count.
		result.Scenes[i].Index = i + 1
	}

	// Catches the "ran out of real facts, started padding" failure mode: the
	// model repeats/paraphrases a fact it already covered instead of adding
	// something new, once it's exhausted the real material for its chosen
	// cases (seen for real: two scenes were word-for-word identical). Pure
	// word-overlap check, no LLM call needed - cheap enough to run on every
	// attempt, and the retry loop in GenerateVideoScript already knows how to
	// feed a specific failure reason back into the next prompt.
	if idx1, idx2, sim := findNearDuplicateScenes(result.Scenes); idx1 != 0 {
		return nil, fmt.Errorf("cenas %d e %d são quase idênticas (%.0f%% de sobreposição de palavras) - o roteiro repetiu conteúdo em vez de trazer fatos novos", idx1, idx2, sim*100)
	}

	// Same "mechanical procedure over model judgment" principle as the
	// near-duplicate check above (see also github.com/AgriciDaniel/anti-slop):
	// a generic filler phrase alone doesn't condemn a scene, but a script
	// leaning on several of them is a cheap, reliable, deterministic signal
	// that the Roteirista defaulted to a stock phrase instead of a concrete
	// fact - worth catching here instead of hoping the LLM supervisor
	// (runSupervisorCheck below) happens to notice it on its own.
	if slopCount, examples := detectSlopPhrases(result.Scenes); slopCount >= 3 && slopCount > len(result.Scenes)/8 {
		return nil, fmt.Errorf("roteiro usa clichês genéricos de narração de IA em excesso (%d ocorrências) em vez de linguagem factual específica - exemplos: %s. Reescreva essas cenas substituindo essas frases prontas por fatos concretos (datas, nomes, números, detalhes do evento)", slopCount, strings.Join(examples, "; "))
	}

	return &result, nil
}

// slopPhrasePatterns are generic filler/hook phrases that Portuguese LLM
// narration reaches for by default instead of a concrete fact - the
// "vocabulary excess" marker concept from github.com/AgriciDaniel/anti-slop,
// adapted to Portuguese and to this project's dark-channel documentary
// narration style (curated from phrases actually observed in this project's
// own generated scripts and bookend intros/outros).
var slopPhrasePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)mergulh(ar|e|amos)\s+(fundo\s+)?n[ao]s?\s`),
	regexp.MustCompile(`(?i)prepare-se para`),
	regexp.MustCompile(`(?i)uma jornada (emocionante|incr[íi]vel|fascinante|[úu]nica)`),
	regexp.MustCompile(`(?i)ao longo (da hist[óo]ria|dos s[ée]culos|dos anos|do tempo)`),
	regexp.MustCompile(`(?i)[ée] importante (ressaltar|destacar|notar|mencionar) que`),
	regexp.MustCompile(`(?i)vale (a pena )?(ressaltar|destacar|mencionar)`),
	regexp.MustCompile(`(?i)n[ãa]o [ée] (à toa|por acaso) que`),
	regexp.MustCompile(`(?i)desvendar os (mist[ée]rios|segredos)`),
	regexp.MustCompile(`(?i)intrigam? a humanidade`),
	regexp.MustCompile(`(?i)at[ée] hoje,? (ningu[ée]m sabe|permanece um mist[ée]rio|sem explica[çc][ãa]o)`),
	regexp.MustCompile(`(?i)o que (torna|faz com que|deixa) (isso |a hist[óo]ria )?ainda mais`),
	regexp.MustCompile(`(?i)nos bastidores`),
	regexp.MustCompile(`(?i)por tr[áa]s (dos|das) (cortinas|panos)`),
	regexp.MustCompile(`(?i)uma verdadeira montanha-russa`),
	regexp.MustCompile(`(?i)deixou (o mundo|a comunidade cient[íi]fica|os investigadores) (perplex[oa]s?|intrigad[oa]s?|chocad[oa]s?)`),
	regexp.MustCompile(`(?i)sem sombra de d[úu]vida`),
	regexp.MustCompile(`(?i)o que ningu[ée]m esperava`),
	regexp.MustCompile(`(?i)guarda(m)? (um )?segredos? (sombrio|obscuro)?`),
	regexp.MustCompile(`(?i)fen[ôo]menos? inexplic[áa]ve(l|is)`),
	regexp.MustCompile(`(?i)hist[óo]rias? esquecidas?`),
	regexp.MustCompile(`(?i)desafia(m)? (a l[óo]gica|a explica[çc][ãa]o|toda l[óo]gica)`),
	regexp.MustCompile(`(?i)o que aconteceu (a seguir|em seguida) (mudaria|chocaria|abalaria)`),
	regexp.MustCompile(`(?i)uma teia de (mist[ée]rios|intrigas|segredos)`),
	regexp.MustCompile(`(?i)fronteira entre o real e o inexplic[áa]vel`),
}

// detectSlopPhrases scans every scene's narration for slopPhrasePatterns and
// returns the total match count plus a few unique examples (scene index +
// matched text) for feedback. A handful of matches across a 35-55 scene
// script is normal Portuguese; the retry threshold in the caller only trips
// once matches are dense enough to look like a habit, not a coincidence.
func detectSlopPhrases(scenes []SceneOutput) (int, []string) {
	count := 0
	seen := make(map[string]bool)
	var examples []string
	for _, s := range scenes {
		for _, re := range slopPhrasePatterns {
			m := re.FindString(s.NarrationText)
			if m == "" {
				continue
			}
			count++
			key := strings.ToLower(m)
			if !seen[key] && len(examples) < 6 {
				seen[key] = true
				examples = append(examples, fmt.Sprintf("cena %d: %q", s.Index, m))
			}
		}
	}
	return count, examples
}

// findNearDuplicateScenes returns the indexes of the first pair of scenes
// whose narration overlaps enough to be considered a repeat, and their
// similarity score. Returns (0, 0, 0) if no pair crosses the threshold.
func findNearDuplicateScenes(scenes []SceneOutput) (int, int, float64) {
	const similarityThreshold = 0.6
	wordSets := make([]map[string]bool, len(scenes))
	for i, s := range scenes {
		wordSets[i] = significantWordSet(s.NarrationText)
	}

	for i := 0; i < len(scenes); i++ {
		for j := i + 1; j < len(scenes); j++ {
			sim := jaccardSimilarity(wordSets[i], wordSets[j])
			if sim >= similarityThreshold {
				return scenes[i].Index, scenes[j].Index, sim
			}
		}
	}
	return 0, 0, 0
}

// significantWordSet lowercases, strips basic punctuation, and drops very
// short words (mostly articles/prepositions) so comparisons key on the words
// that actually carry meaning (names, facts, dates) rather than grammar glue
// that any two unrelated Portuguese sentences would share anyway.
func significantWordSet(text string) map[string]bool {
	replacer := strings.NewReplacer(".", "", ",", "", "!", "", "?", "", "-", " ", "–", " ", "'", "", "\"", "")
	cleaned := replacer.Replace(strings.ToLower(text))
	set := make(map[string]bool)
	for _, w := range strings.Fields(cleaned) {
		if len(w) > 3 {
			set[w] = true
		}
	}
	return set
}

// jaccardSimilarity is the intersection-over-union of two word sets - 1.0
// means identical vocabulary, 0.0 means nothing in common.
func jaccardSimilarity(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for w := range a {
		if b[w] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// isBrandingCategory marks the images that define how a channel looks -
// generated rarely, seen constantly - as opposed to bulk per-scene images.
func isBrandingCategory(category string) bool {
	return category == "logo" || category == "banner" || category == "thumbnail"
}

// GenerateImage routes image generation by category:
//
//   - logo / banner / thumbnail (branding-critical, a handful per channel):
//     OpenAI gpt-image first (best quality, follows layout/text instructions),
//     then Gemini. Pollinations is used ONLY when neither key is configured -
//     if a configured provider fails (quota, billing, invalid key) the caller
//     gets that real error back instead of a silently worse image.
//   - scene_* (bulk: dozens per video, and only reached after Pexels had no
//     usable photo): Pollinations first (free), Gemini as fallback. OpenAI is
//     deliberately NOT used here - at ~$0.03-0.20/image it would turn every
//     video into several dollars of image spend.
func GenerateImage(channelID, videoID, prompt string, category string) (string, error) {
	openAIKey := config.AppConfig.OpenAIAPIKey
	geminiKey := config.AppConfig.GeminiAPIKey
	var failures []string

	if isBrandingCategory(category) {
		if config.IsSet(openAIKey) {
			path, err := generateOpenAIImage(channelID, videoID, prompt, category, openAIKey)
			if err == nil && path != "" {
				return path, nil
			}
			log.Printf("OpenAI image generation failed (%s): %v", category, err)
			failures = append(failures, "OpenAI: "+errString(err))
		}
		if config.IsSet(geminiKey) {
			path, err := generateGeminiImage(channelID, videoID, prompt, category, geminiKey)
			if err == nil && path != "" {
				return path, nil
			}
			log.Printf("Gemini image generation failed (%s): %v", category, err)
			failures = append(failures, "Gemini: "+errString(err))
		}
		if len(failures) > 0 {
			return "", fmt.Errorf("falha ao gerar imagem (%s) - %s", category, strings.Join(failures, " | "))
		}
		log.Printf("WARNING: no OpenAI/Gemini key configured - generating %s with Pollinations (lower quality)", category)
		return generatePollinationsImage(prompt, category)
	}

	path, err := generatePollinationsImage(prompt, category)
	if err == nil && path != "" {
		return path, nil
	}
	failures = append(failures, "Pollinations: "+errString(err))
	if config.IsSet(geminiKey) {
		path, gErr := generateGeminiImage(channelID, videoID, prompt, category, geminiKey)
		if gErr == nil && path != "" {
			return path, nil
		}
		failures = append(failures, "Gemini: "+errString(gErr))
	}
	return "", fmt.Errorf("falha ao gerar imagem de cena - %s", strings.Join(failures, " | "))
}

func errString(err error) string {
	if err == nil {
		return "resposta vazia"
	}
	return err.Error()
}

// BuildLogoPrompt turns the branding LLM's short visual idea into a full
// art-direction brief. The old prompt ("A sleek minimalist vector logo
// depicting <niche>, 4k") gave image models nothing to work with, which is
// why logos came out generic: no constraints on legibility at avatar size,
// circular cropping, palette or style.
func BuildLogoPrompt(channelName, niche, concept, visualIdea string) string {
	if strings.TrimSpace(visualIdea) == "" {
		visualIdea = "a symbol that captures the essence of " + niche
	}
	return fmt.Sprintf(`Design a premium YouTube channel avatar logo for the channel "%s" (niche: %s).
Channel concept: %s
Visual idea for the emblem: %s

Art direction:
- One bold, iconic emblem/symbol centered on a square canvas, with generous padding so it survives a circular crop.
- Clean modern vector / flat-illustration style, crisp edges, strong readable silhouette, at most three harmonious colors plus the background.
- Must stay instantly recognizable at 48x48 pixels - no thin lines or tiny details.
- Solid or very subtle gradient background in a color that contrasts with the emblem; palette and mood must fit the niche.
- Agency-level brand identity quality.
Strictly no text, letters, numbers, signatures, watermarks, mockup frames or real-world brand logos.`,
		channelName, niche, strings.TrimSpace(concept), strings.TrimSpace(visualIdea))
}

// BuildBannerPrompt is the channel-cover counterpart of BuildLogoPrompt.
func BuildBannerPrompt(channelName, niche, concept, visualIdea string) string {
	if strings.TrimSpace(visualIdea) == "" {
		visualIdea = "a cinematic scene that represents " + niche
	}
	return fmt.Sprintf(`Design the YouTube channel banner (cover art) for the channel "%s" (niche: %s).
Channel concept: %s
Scene / visual idea: %s

Art direction:
- Wide, cinematic landscape composition, polished key-art quality with rich lighting, depth and a cohesive color palette that fits the niche.
- YouTube crops the top, bottom and sides on phones and TVs: keep the title and the main subject inside the horizontal center band; the edges should be atmospheric background only.
- Render the channel name "%s" exactly once as a large, bold, perfectly spelled title (keep every accent), in a typeface that matches the niche, with strong contrast against the background.
- No other text, no taglines, no watermarks, no UI or device mockups, no real-world brand logos.`,
		channelName, niche, strings.TrimSpace(concept), strings.TrimSpace(visualIdea), channelName)
}

// generateGeminiImage generates an image with Gemini 3.1 Flash Lite Image
// ("Nano Banana 2 Lite") - the cheapest/fastest model in the Gemini image
// family, picked specifically to keep per-image cost and latency low enough
// to sustain 50-100 generations/day. 1K is the smallest imageSize the API
// offers (there's no lower-resolution tier to drop to).
func generateGeminiImage(channelID, videoID, prompt, category, apiKey string) (string, error) {
	aspectRatio := "1:1"
	if category == "banner" || category == "thumbnail" || strings.HasPrefix(category, "scene_") {
		aspectRatio = "16:9"
	}

	reqPayload := GeminiImageRequest{
		Contents: []GeminiContent{
			{Parts: []GeminiPart{{Text: prompt}}},
		},
		GenerationConfig: GeminiImageGenConfig{
			ResponseModalities: []string{"IMAGE"},
			ImageConfig: GeminiImageOpts{
				AspectRatio: aspectRatio,
				ImageSize:   "1K",
			},
		},
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-3.1-flash-lite-image:generateContent?key=%s", apiKey)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini image api returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var geminiResp GeminiImageResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return "", err
	}

	for _, candidate := range geminiResp.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.InlineData == nil || part.InlineData.Data == "" {
				continue
			}
			imgBytes, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
			if err != nil {
				return "", fmt.Errorf("failed to decode gemini image base64: %w", err)
			}
			RecordGeminiImageGeneration(channelID, videoID)
			fileName := fmt.Sprintf("gemini_%d.jpg", time.Now().UnixNano())
			return SaveMedia(imgBytes, "images/"+fileName, "image/jpeg")
		}
	}

	return "", fmt.Errorf("gemini image api returned no image data")
}

func generatePollinationsImage(prompt, category string) (string, error) {
	width, height := 1280, 720
	if category == "logo" {
		width, height = 1024, 1024
	} else if category == "banner" {
		width, height = 1200, 400
	}
	encodedPrompt := url.QueryEscape(prompt)

	// Use FLUX model on Pollinations for high-resolution cinematic output
	imageURL := fmt.Sprintf("https://image.pollinations.ai/prompt/%s?width=%d&height=%d&model=flux&nologo=true&seed=%d",
		encodedPrompt, width, height, time.Now().UnixNano()%1000000)

	req, err := http.NewRequest("GET", imageURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create image request: %w", err)
	}

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return "", fmt.Errorf("pollinations image generation failed (status %d): %v", func() int {
			if resp != nil {
				return resp.StatusCode
			}
			return 0
		}(), err)
	}
	defer resp.Body.Close()

	fileName := fmt.Sprintf("pollinations_%d.jpg", time.Now().UnixNano())
	return SaveMediaFromReader(resp.Body, "images/"+fileName, "image/jpeg")
}

// openAIImageParams picks size/quality/format per category. Logos and
// banners are generated once per channel, so they get "high" quality;
// thumbnails are per video, so "medium" keeps that cost reasonable.
// OPENAI_IMAGE_QUALITY (low|medium|high) overrides both.
func openAIImageParams(category string) (size, quality, format string) {
	size, quality, format = "1536x1024", "high", "jpeg"
	switch category {
	case "logo":
		size, format = "1024x1024", "png"
	case "thumbnail":
		quality = "medium"
	}
	if q := strings.ToLower(strings.TrimSpace(os.Getenv("OPENAI_IMAGE_QUALITY"))); q == "low" || q == "medium" || q == "high" {
		quality = q
	}
	return size, quality, format
}

// generateOpenAIImage calls the OpenAI Images API (gpt-image family). The
// gpt-image models don't accept "response_format" - they always return
// b64_json, which is decoded below. OPENAI_IMAGE_MODEL overrides the model.
func generateOpenAIImage(channelID, videoID, prompt, category, apiKey string) (string, error) {
	url := "https://api.openai.com/v1/images/generations"

	model := strings.TrimSpace(os.Getenv("OPENAI_IMAGE_MODEL"))
	if model == "" {
		model = "gpt-image-1.5"
	}
	size, quality, format := openAIImageParams(category)

	reqPayload := map[string]interface{}{
		"model":         model,
		"prompt":        prompt,
		"n":             1,
		"size":          size,
		"quality":       quality,
		"output_format": format,
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// High-quality gpt-image renders routinely take 30-90s; the old 60s
	// timeout cut off exactly the requests worth waiting for.
	client := &http.Client{Timeout: 4 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openai images api (%s) returned status %d: %s", model, resp.StatusCode, string(respBody))
	}

	var imgResp struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respBody, &imgResp); err != nil {
		return "", err
	}

	if len(imgResp.Data) == 0 {
		return "", fmt.Errorf("openai images api returned no images")
	}

	RecordImageGeneration(channelID, videoID, openAIImageCost(quality, size))

	if imgResp.Data[0].B64JSON != "" {
		imgBytes, err := base64.StdEncoding.DecodeString(imgResp.Data[0].B64JSON)
		if err != nil {
			return "", fmt.Errorf("failed to decode openai base64 image: %v", err)
		}
		// Extension and Content-Type now match the bytes actually returned
		// (logos used to be PNG data saved as ".jpg" / image/jpeg).
		ext, contentType := ".jpg", "image/jpeg"
		if format == "png" {
			ext, contentType = ".png", "image/png"
		}
		fileName := fmt.Sprintf("%s_openai_%d%s", category, time.Now().UnixNano(), ext)
		return SaveMedia(imgBytes, "images/"+fileName, contentType)
	}

	return downloadImage(imgResp.Data[0].URL)
}

func downloadImage(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download image, status %d", resp.StatusCode)
	}

	fileName := fmt.Sprintf("img_%d.jpg", time.Now().UnixNano())
	return SaveMediaFromReader(resp.Body, "images/"+fileName, "image/jpeg")
}

// GenerateVoiceOver calls ElevenLabs, OpenAI TTS, or Free TTS fallback
func GenerateVoiceOver(channelID, videoID, text string, sceneIndex int) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("empty narration text")
	}

	apiKey := config.AppConfig.ElevenLabsAPIKey
	if config.IsSet(apiKey) {
		audio, err := generateElevenLabsTTS(channelID, videoID, text, sceneIndex)
		if err == nil && audio != "" {
			return audio, nil
		}
		log.Printf("ElevenLabs TTS failed: %v. Trying OpenAI TTS...", err)
	}

	openAIKey := config.AppConfig.OpenAIAPIKey
	if config.IsSet(openAIKey) {
		audio, err := generateOpenAITTS(channelID, videoID, text, sceneIndex, openAIKey)
		if err == nil && audio != "" {
			return audio, nil
		}
		log.Printf("OpenAI TTS failed: %v. Trying Free TTS fallback...", err)
	}

	return generateFreeTTS(channelID, videoID, text, sceneIndex)
}

func generateFreeTTS(channelID, videoID, text string, sceneIndex int) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("empty text for TTS")
	}

	ttsText := text
	if len([]rune(ttsText)) > 200 {
		ttsText = string([]rune(ttsText)[:200])
	}

	ttsURL := fmt.Sprintf("https://translate.google.com/translate_tts?ie=UTF-8&client=tw-ob&tl=pt&q=%s", url.QueryEscape(ttsText))
	req, err := http.NewRequest("GET", ttsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("free tts status %d", resp.StatusCode)
	}

	fileName := fmt.Sprintf("voice_free_%s_%d_%d.mp3", videoID, sceneIndex, time.Now().UnixNano())
	return SaveMediaFromReader(resp.Body, "audio/"+fileName, "audio/mpeg")
}

func generateElevenLabsTTS(channelID, videoID, text string, sceneIndex int) (string, error) {
	// Adam - deep male voice, fits the "dark channel" narration style.
	// (Previously defaulted to "Rachel", a female voice.) Channels that
	// picked a narrator voice (Settings > Narrador do Canal) override this.
	voiceID := "pNInz6obpgDQGcFmaJgB"
	var channel models.Channel
	if err := db.DB.Select("narrator_voice_id").First(&channel, "id = ?", channelID).Error; err == nil && channel.NarratorVoiceID != "" {
		voiceID = channel.NarratorVoiceID
	}
	url := fmt.Sprintf("https://api.elevenlabs.io/v1/text-to-speech/%s", voiceID)

	reqPayload := map[string]interface{}{
		"text":     text,
		"model_id": "eleven_multilingual_v2",
		"voice_settings": map[string]interface{}{
			"stability":        0.75,
			"similarity_boost": 0.75,
		},
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("xi-api-key", config.AppConfig.ElevenLabsAPIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("elevenlabs returned status %d: %s", resp.StatusCode, string(respBody))
	}

	RecordTTS(channelID, videoID, "elevenlabs", len(text))

	fileName := fmt.Sprintf("narration_%d_%d.mp3", sceneIndex, time.Now().UnixNano())
	return SaveMediaFromReader(resp.Body, "audio/"+fileName, "audio/mpeg")
}

func generateOpenAITTS(channelID, videoID, text string, sceneIndex int, apiKey string) (string, error) {
	url := "https://api.openai.com/v1/audio/speech"

	reqPayload := map[string]interface{}{
		"model": "tts-1",
		"input": text,
		"voice": "onyx", // onyx is deep and great for dark channels
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("openai tts returned status %d: %s", resp.StatusCode, string(respBody))
	}

	RecordTTS(channelID, videoID, "openai", len(text))

	fileName := fmt.Sprintf("narration_%d_%d.mp3", sceneIndex, time.Now().UnixNano())
	return SaveMediaFromReader(resp.Body, "audio/"+fileName, "audio/mpeg")
}
