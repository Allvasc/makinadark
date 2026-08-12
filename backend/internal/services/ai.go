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
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// CallLLM sends a prompt to Groq, Gemini, Claude, or OpenAI, requesting a JSON response.
// channelID/videoID attribute the call's cost to the cost dashboard - videoID
// may be empty for channel-level calls (branding, trends) not tied to one video.
func CallLLM(channelID, videoID, prompt string) (string, error) {
	groqKey := config.AppConfig.GroqAPIKey
	groqConfigured := groqKey != "" && groqKey != "your_groq_api_key_here"

	claudeKey := config.AppConfig.AnthropicAPIKey
	claudeConfigured := claudeKey != "" && claudeKey != "your_anthropic_api_key_here"

	openAIKey := config.AppConfig.OpenAIAPIKey
	openAIConfigured := openAIKey != "" && openAIKey != "your_openai_api_key_here"

	geminiKey := config.AppConfig.GeminiAPIKey
	geminiConfigured := geminiKey != "" && geminiKey != "your_gemini_api_key_here"

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
		result, err := callGroq(channelID, videoID, prompt, groqKey)
		if err == nil {
			return result, nil
		}
		log.Printf("Groq call failed: %v", err)
		failures = append(failures, "Groq: "+err.Error())
	}

	if geminiConfigured {
		result, err := callGemini(channelID, videoID, prompt, geminiKey)
		if err == nil {
			return result, nil
		}
		log.Printf("Gemini call failed: %v", err)
		failures = append(failures, "Gemini: "+err.Error())
	}

	if claudeConfigured {
		result, err := callClaude(channelID, videoID, prompt, claudeKey)
		if err == nil {
			return result, nil
		}
		log.Printf("Claude call failed: %v", err)
		failures = append(failures, "Claude: "+err.Error())
	}

	if openAIConfigured {
		result, err := callOpenAI(channelID, videoID, prompt, openAIKey)
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

func callGroq(channelID, videoID, prompt string, apiKey string) (string, error) {
	url := "https://api.groq.com/openai/v1/chat/completions"

	reqPayload := OpenAIRequest{
		Model: "llama-3.3-70b-versatile",
		Messages: []OpenAIMessage{
			{Role: "user", Content: prompt},
		},
		ResponseFormat: &OpenAIFormat{Type: "json_object"},
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

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("groq api returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var groqResp OpenAIResponse
	if err := json.Unmarshal(respBody, &groqResp); err != nil {
		return "", err
	}

	if len(groqResp.Choices) == 0 {
		return "", fmt.Errorf("groq api returned empty choices")
	}

	RecordGroqTokens(channelID, videoID, groqResp.Usage.PromptTokens, groqResp.Usage.CompletionTokens)

	return groqResp.Choices[0].Message.Content, nil
}

func callClaude(channelID, videoID, prompt string, apiKey string) (string, error) {
	url := "https://api.anthropic.com/v1/messages"

	reqPayload := ClaudeRequest{
		Model: "claude-3-5-sonnet-20241022",
		// 8192 (up from 4096) so a full 35-55 scene / 3000-4500 word video
		// script fits without Claude truncating the JSON mid-object.
		MaxTokens: 8192,
		Messages: []ClaudeMessage{
			{Role: "user", Content: prompt + "\n\nIMPORTANTE: Retorne ESTRITAMENTE um objeto JSON válido."},
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
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 60 * time.Second}
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

	if len(claudeResp.Content) == 0 {
		return "", fmt.Errorf("anthropic claude api returned empty content")
	}

	RecordClaudeTokens(channelID, videoID, claudeResp.Usage.InputTokens, claudeResp.Usage.OutputTokens)

	return claudeResp.Content[0].Text, nil
}

func callGemini(channelID, videoID, prompt string, apiKey string) (string, error) {
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
		GenerationConfig: &GeminiGenConfig{
			ResponseMimeType: "application/json",
		},
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return "", err
	}

	// Recorded on attempt (not just success) since the free-tier daily quota
	// (1500 requests/day on the free-tier flash models, per Google's current
	// published limits) is consumed even by calls that fail.
	RecordGeminiRequest(channelID, videoID)

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(body))
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

func callOpenAI(channelID, videoID, prompt string, apiKey string) (string, error) {
	url := "https://api.openai.com/v1/chat/completions"

	reqPayload := OpenAIRequest{
		Model: "gpt-4o-mini",
		Messages: []OpenAIMessage{
			{Role: "user", Content: prompt},
		},
		ResponseFormat: &OpenAIFormat{Type: "json_object"},
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

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openai api returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var openAIResp OpenAIResponse
	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		return "", err
	}

	if len(openAIResp.Choices) == 0 {
		return "", fmt.Errorf("openai api returned empty choices")
	}

	RecordOpenAITokens(channelID, videoID, openAIResp.Usage.PromptTokens, openAIResp.Usage.CompletionTokens)

	return openAIResp.Choices[0].Message.Content, nil
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

func GenerateVideoScript(channelID, videoID, channelName, niche, topic string) (*VideoScriptResult, error) {
	prompt := fmt.Sprintf(`Você é um roteirista e estrategista sênior especialista em canais Dark no YouTube (estratégia 2026 de alta retenção e monetização).
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
   - ÚLTIMA CENA (CALL TO ACTION NATURAL): chamada fluida para engajamento (comentário provocativo sobre a história real e inscrição).
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

IMPORTANTE: o array "scenes" deve conter TODAS as 35 a 55 cenas do roteiro completo (não resuma nem interrompa antes do fim).`, channelName, niche, topic, niche)

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
				"O roteiro deve ser baseado em histórias/fatos REAIS (nunca filosofia ou ficção genérica), a Cena 1 deve ser um gancho direto sem saudações, o título deve ser magnético mas fiel ao conteúdo, e o roteiro deve ter pelo menos 25 cenas. CRÍTICO: as cenas de desenvolvimento (amostra em development_samples) NÃO podem ser superficiais - reprove se alguma delas apenas anunciar um fato genérico sem data, nome, número, detalhe narrativo ou teoria específica (ex: 'as investigações não encontraram respostas' sozinho, sem dizer o que foi investigado, é raso e deve ser reprovado).",
				fmt.Sprintf(`{"title":%q,"description":%q,"scene_count":%d,"scene_1":%q,"scene_2":%q,"development_samples":[%s]}`,
					result.Title, result.Description, len(result.Scenes), result.Scenes[0].NarrationText, result.Scenes[1].NarrationText, strings.TrimSuffix(samples.String(), ",")))
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

// GenerateImage generates branding-critical images (logo, banner/cover, video
// thumbnails) via the Gemini image API ONLY - no Pollinations/placeholder
// fallback for these, since low-quality Pollinations output for exactly
// these three categories is what prompted the switch to Gemini in the first
// place. If Gemini isn't configured or fails (e.g. depleted AI Studio
// credits), the caller gets the error back instead of a worse image.
// Per-scene illustration images stay on Pollinations - they're generated in
// bulk (multiple per video) and don't need the same visual bar. OpenAI is
// completely disabled for image generation to eliminate credit/quota errors.
func GenerateImage(channelID, videoID, prompt string, category string) (string, error) {
	// Try Gemini image model first if API key is configured
	geminiKey := config.AppConfig.GeminiAPIKey
	if geminiKey != "" && geminiKey != "your_gemini_api_key_here" {
		if path, err := generateGeminiImage(channelID, videoID, prompt, category, geminiKey); err == nil && path != "" {
			return path, nil
		}
	}

	// Try OpenAI DALL-E if key is available
	openAIKey := config.AppConfig.OpenAIAPIKey
	if openAIKey != "" && openAIKey != "your_openai_api_key_here" {
		if path, err := generateDallEImage(channelID, videoID, prompt, openAIKey); err == nil && path != "" {
			return path, nil
		}
	}

	return generatePollinationsImage(prompt, category)
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

	client := &http.Client{Timeout: 15 * time.Second}
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

func generateDallEImage(channelID, videoID, prompt string, apiKey string) (string, error) {
	url := "https://api.openai.com/v1/images/generations"

	// Uses gpt-image-1.5 instead of dall-e-3: several projects/orgs no longer
	// have dall-e-3 enabled ("model does not exist"), and the gpt-image family
	// doesn't accept "response_format" - it always returns b64_json, which is
	// what we decode below. gpt-image-1 (the older sibling) is being retired
	// 2026-10-23, and 1.5 is also cheaper ($0.034 vs $0.042 per 1024x1024
	// medium-quality image), so we go straight to the newer model.
	reqPayload := map[string]interface{}{
		"model":  "gpt-image-1.5",
		"prompt": prompt,
		"n":      1,
		"size":   "1024x1024",
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

	client := &http.Client{Timeout: 60 * time.Second}
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
		return "", fmt.Errorf("dall-e api returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var dallEResp struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respBody, &dallEResp); err != nil {
		return "", err
	}

	if len(dallEResp.Data) == 0 {
		return "", fmt.Errorf("dall-e returned no images")
	}

	RecordImageGeneration(channelID, videoID)

	if dallEResp.Data[0].B64JSON != "" {
		imgBytes, err := base64.StdEncoding.DecodeString(dallEResp.Data[0].B64JSON)
		if err != nil {
			return "", fmt.Errorf("failed to decode dall-e base64 image: %v", err)
		}
		fileName := fmt.Sprintf("img_%d.jpg", time.Now().UnixNano())
		return SaveMedia(imgBytes, "images/"+fileName, "image/jpeg")
	}

	return downloadImage(dallEResp.Data[0].URL)
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
	if apiKey != "" && apiKey != "your_elevenlabs_api_key_here" {
		audio, err := generateElevenLabsTTS(channelID, videoID, text, sceneIndex)
		if err == nil && audio != "" {
			return audio, nil
		}
		log.Printf("ElevenLabs TTS failed: %v. Trying OpenAI TTS...", err)
	}

	openAIKey := config.AppConfig.OpenAIAPIKey
	if openAIKey != "" && openAIKey != "your_openai_api_key_here" {
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
