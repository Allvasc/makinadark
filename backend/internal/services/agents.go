package services

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// This file implements the specialized-agent pipeline that runs after the
// Roteirista (GenerateVideoScript, in ai.go) writes the initial script:
// Revisor de Textos -> Diretor de Cinema -> Sonoplastia -> Especialista em
// YouTube/Tendências. Each stage is a worker LLM call followed by a
// supervisor LLM call that approves it or hands back feedback for exactly
// one retry - the same bounded-retry shape already used for the Roteirista's
// own JSON validation, so cost/latency stay predictable instead of looping
// forever. A stage (worker OR supervisor) that fails outright falls back to
// the input unchanged and logs a warning - these are quality-improvement
// passes on top of an already-valid script, not new hard-failure points.

// supervisorVerdict is the common response shape every supervisor prompt
// returns.
type supervisorVerdict struct {
	Approved bool   `json:"approved"`
	Feedback string `json:"feedback"`
}

// runSupervisorCheck sends a worker's output to a supervisor LLM call with
// the given rubric. A supervisor failure (LLM error, bad JSON) auto-approves
// rather than blocking the pipeline on the supervisor's own hiccup.
func runSupervisorCheck(channelID, videoID, role, rubric, outputJSON string) supervisorVerdict {
	prompt := fmt.Sprintf(`Você é o supervisor de %s numa produtora de vídeos para YouTube, revisando o trabalho de outro agente de IA antes que ele siga para a próxima etapa.

Critérios de aprovação:
%s

Trabalho a ser revisado (JSON):
%s

Retorne estritamente um JSON: {"approved": true ou false, "feedback": "o que corrigir, se houver problema, ou string vazia se aprovado"}`, role, rubric, outputJSON)

	jsonStr, err := CallLLM(channelID, videoID, prompt)
	if err != nil {
		log.Printf("Supervisor (%s) call failed, auto-approving: %v", role, err)
		return supervisorVerdict{Approved: true}
	}

	var verdict supervisorVerdict
	if err := json.Unmarshal([]byte(stripJSONFences(jsonStr)), &verdict); err != nil {
		log.Printf("Supervisor (%s) response unparseable, auto-approving: %v", role, err)
		return supervisorVerdict{Approved: true}
	}

	return verdict
}

// ---------------------------------------------------------------------------
// Revisor de Textos - proofreads narration text for spelling/grammar errors,
// corrupted/mis-encoded characters, and stray stage-direction text that
// would otherwise be spoken aloud by the TTS and burned into the subtitles
// verbatim.
// ---------------------------------------------------------------------------

type sceneTextItem struct {
	Index int    `json:"index"`
	Text  string `json:"narration_text"`
}

func reviewScriptText(channelID, videoID string, script *VideoScriptResult) *VideoScriptResult {
	feedback := ""
	for attempt := 1; attempt <= 2; attempt++ {
		reviewed, itemsJSON, err := runTextReviewPass(channelID, videoID, script, feedback)
		if err != nil {
			log.Printf("Revisor de Textos attempt %d failed, keeping previous narration: %v", attempt, err)
			return script
		}

		verdict := runSupervisorCheck(channelID, videoID, "revisão de textos",
			"O texto deve estar gramaticalmente correto, sem caracteres corrompidos/mal codificados, sem repetições acidentais de palavras, sem direções de cena entre colchetes ou parênteses, e o SENTIDO e o TAMANHO aproximado de cada texto original devem ter sido preservados.",
			itemsJSON)
		if verdict.Approved || attempt == 2 {
			if !verdict.Approved {
				log.Printf("Revisor de Textos: supervisor feedback not resolved after retry, using latest revision anyway: %s", verdict.Feedback)
			}
			return reviewed
		}
		feedback = verdict.Feedback
		script = reviewed // retry from the improved version, not from scratch
	}
	return script
}

func runTextReviewPass(channelID, videoID string, script *VideoScriptResult, priorFeedback string) (*VideoScriptResult, string, error) {
	items := make([]sceneTextItem, 0, len(script.Scenes))
	for _, s := range script.Scenes {
		items = append(items, sceneTextItem{Index: s.Index, Text: s.NarrationText})
	}
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, "", err
	}

	feedbackNote := ""
	if priorFeedback != "" {
		feedbackNote = "\n\nFeedback do supervisor na revisão anterior, aplique antes de responder: " + priorFeedback
	}

	prompt := fmt.Sprintf(`Você é um revisor de textos profissional, especializado em roteiros para narração em vídeo (TTS) e legendas em português do Brasil.

Revise CADA texto de narração abaixo e corrija: erros de ortografia e gramática, caracteres corrompidos ou substituídos por símbolos estranhos no lugar de letras acentuadas, pontuação que atrapalha a leitura por TTS, repetições acidentais de palavras, e qualquer trecho que pareça direção de cena entre colchetes ou parênteses em vez de texto para ser narrado.

NÃO mude o sentido, os fatos, nem o tamanho aproximado de cada texto. NÃO pule nenhum índice.%s

Textos originais (JSON):
%s

Retorne estritamente um JSON no formato {"scenes":[{"index":1,"narration_text":"texto corrigido..."}]} com TODOS os índices presentes.`, feedbackNote, string(itemsJSON))

	jsonStr, err := CallLLM(channelID, videoID, prompt)
	if err != nil {
		return nil, "", err
	}

	var resp struct {
		Scenes []sceneTextItem `json:"scenes"`
	}
	if err := json.Unmarshal([]byte(stripJSONFences(jsonStr)), &resp); err != nil {
		return nil, "", fmt.Errorf("failed to parse review response: %w", err)
	}
	if len(resp.Scenes) == 0 {
		return nil, "", fmt.Errorf("review response has no scenes")
	}

	corrected := make(map[int]string, len(resp.Scenes))
	for _, item := range resp.Scenes {
		if strings.TrimSpace(item.Text) != "" {
			corrected[item.Index] = item.Text
		}
	}

	reviewed := *script
	reviewed.Scenes = make([]SceneOutput, len(script.Scenes))
	copy(reviewed.Scenes, script.Scenes)
	applied := 0
	for i := range reviewed.Scenes {
		if text, ok := corrected[reviewed.Scenes[i].Index]; ok {
			reviewed.Scenes[i].NarrationText = text
			applied++
		}
	}
	if applied == 0 {
		return nil, "", fmt.Errorf("review response matched no scene indexes")
	}

	appliedJSON, _ := json.Marshal(reviewed.Scenes)
	return &reviewed, string(appliedJSON), nil
}

// ---------------------------------------------------------------------------
// Revisor de Textos (abertura/encerramento) - the same proofreading rubric
// as reviewScriptText above, applied to a channel's single-line intro/outro
// narration (bookends.go) instead of a scene array. Runs on both AI-written
// and user-typed text, same as the main script - a human-typed CTA still
// benefits from a grammar/encoding pass before it reaches TTS and subtitles.
// ---------------------------------------------------------------------------

func reviewBookendText(channelID, kind, text string) string {
	current := strings.TrimSpace(text)
	if current == "" {
		return current
	}
	feedback := ""
	for attempt := 1; attempt <= 2; attempt++ {
		reviewed, err := runBookendTextReviewPass(channelID, kind, current, feedback)
		if err != nil {
			log.Printf("Revisor de Textos (%s) attempt %d failed, keeping previous text: %v", kind, attempt, err)
			return current
		}

		itemJSON, _ := json.Marshal(map[string]string{"text": reviewed})
		verdict := runSupervisorCheck(channelID, "", "revisão de textos",
			"O texto deve estar gramaticalmente correto, sem caracteres corrompidos/mal codificados, sem repetições acidentais de palavras, sem direções de cena entre colchetes ou parênteses, e o SENTIDO e o TAMANHO aproximado do texto original devem ter sido preservados.",
			string(itemJSON))
		if verdict.Approved || attempt == 2 {
			if !verdict.Approved {
				log.Printf("Revisor de Textos (%s): supervisor feedback not resolved after retry, using latest revision anyway: %s", kind, verdict.Feedback)
			}
			return reviewed
		}
		feedback = verdict.Feedback
		current = reviewed
	}
	return current
}

func runBookendTextReviewPass(channelID, kind, text, priorFeedback string) (string, error) {
	feedbackNote := ""
	if priorFeedback != "" {
		feedbackNote = "\n\nFeedback do supervisor na revisão anterior, aplique antes de responder: " + priorFeedback
	}

	prompt := fmt.Sprintf(`Você é um revisor de textos profissional, especializado em roteiros para narração em vídeo (TTS) em português do Brasil.

Revise o texto de %s abaixo e corrija: erros de ortografia e gramática, caracteres corrompidos ou substituídos por símbolos estranhos no lugar de letras acentuadas, pontuação que atrapalha a leitura por TTS, e repetições acidentais de palavras.

NÃO mude o sentido nem o tamanho aproximado do texto.%s

Texto original: %q

Retorne estritamente um JSON: {"text":"texto corrigido..."}`, kind, feedbackNote, text)

	jsonStr, err := CallLLM(channelID, "", prompt)
	if err != nil {
		return "", err
	}

	var resp struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(stripJSONFences(jsonStr)), &resp); err != nil {
		return "", fmt.Errorf("failed to parse bookend review response: %w", err)
	}
	if strings.TrimSpace(resp.Text) == "" {
		return "", fmt.Errorf("bookend review response empty")
	}
	return strings.TrimSpace(resp.Text), nil
}

// ---------------------------------------------------------------------------
// Diretor de Cinema - writes stronger, more consistent image prompts per
// scene, decides the video's overall music mood and whether scenes fade
// in/out at cuts, and edits in a connective transition line whenever a scene
// jumps to a new subject/chapter, so the cut doesn't feel abrupt.
// ---------------------------------------------------------------------------

type directorNotes struct {
	MusicMood          string `json:"music_mood"` // "suspense", "business", or "ambient"
	UseFadeTransitions bool   `json:"use_fade_transitions"`
	Scenes             []struct {
		Index          int    `json:"index"`
		ImagePrompt    string `json:"image_prompt"`
		TransitionText string `json:"transition_text"` // short bridging phrase to prepend to this scene's narration, ONLY when it opens a new subject/chapter; empty otherwise
	} `json:"scenes"`
}

func applyDirectorNotes(channelID, videoID string, script *VideoScriptResult, niche string) *directorNotes {
	feedback := ""
	for attempt := 1; attempt <= 2; attempt++ {
		notes, notesJSON, err := runDirectorPass(channelID, videoID, script, niche, feedback)
		if err != nil {
			log.Printf("Diretor de Cinema attempt %d failed, keeping original image prompts: %v", attempt, err)
			return nil
		}

		verdict := runSupervisorCheck(channelID, videoID, "direção de cinema",
			"Cada image_prompt deve estar em INGLÊS, ser cinematográfico e coerente com a narração daquela cena específica, sem inserir texto na imagem. Cada transition_text (quando não vazio) deve estar em PORTUGUÊS DO BRASIL - reprove se algum transition_text estiver em inglês ou qualquer outro idioma. music_mood deve ser exatamente \"suspense\", \"business\" ou \"ambient\". Toda cena que muda de assunto/capítulo em relação à cena anterior deve ter um transition_text curto e natural conectando os dois assuntos - cenas que continuam o mesmo assunto devem ter transition_text vazio.",
			notesJSON)
		if verdict.Approved || attempt == 2 {
			if !verdict.Approved {
				log.Printf("Diretor de Cinema: supervisor feedback not resolved after retry, using latest version anyway: %s", verdict.Feedback)
			}
			return notes
		}
		feedback = verdict.Feedback
	}
	return nil
}

func runDirectorPass(channelID, videoID string, script *VideoScriptResult, niche, priorFeedback string) (*directorNotes, string, error) {
	type sceneBrief struct {
		Index int    `json:"index"`
		Text  string `json:"narration_text"`
	}
	briefs := make([]sceneBrief, 0, len(script.Scenes))
	for _, s := range script.Scenes {
		briefs = append(briefs, sceneBrief{Index: s.Index, Text: s.NarrationText})
	}
	briefsJSON, err := json.Marshal(briefs)
	if err != nil {
		return nil, "", err
	}

	feedbackNote := ""
	if priorFeedback != "" {
		feedbackNote = "\n\nFeedback do supervisor na tentativa anterior, aplique antes de responder: " + priorFeedback
	}

	prompt := fmt.Sprintf(`Você é um diretor de cinema especializado em vídeos documentais/dark para YouTube, no nicho '%s'. Sua responsabilidade inclui a CONTINUIDADE do roteiro: o roteirista escreve cena por cena e às vezes pula de um assunto pra outro sem transição, o que quebra o ritmo do vídeo.

ATENÇÃO A IDIOMAS - os dois campos de texto abaixo usam idiomas DIFERENTES, não confunda:
- "image_prompt" -> SEMPRE em INGLÊS (é para um gerador de imagem por IA).
- "transition_text" -> SEMPRE em PORTUGUÊS DO BRASIL (vai ser lida em voz alta pelo narrador do vídeo, junto com o resto da narração em português). NUNCA escreva transition_text em inglês.

Com base na narração de cada cena abaixo, para CADA cena decida:
- "image_prompt": cinematográfico, em inglês, coerente com o conteúdo narrado daquela cena específica, ultra detalhado, 4k, sem texto na imagem.
- "transition_text": leia a cena anterior e a atual. Se a cena atual COMEÇA UM ASSUNTO/CAPÍTULO NOVO (ex: terminou de falar de um mistério e começa outro sem nenhuma ligação), escreva EM PORTUGUÊS uma frase CURTA, COMPLETA E INDEPENDENTE que conecte os dois assuntos (ex: "Mas esse não foi o único mistério que desafiou explicações.", "Enquanto isso, do outro lado do mundo, algo parecido acontecia.", "Essa não foi a única vez que isso aconteceu."). Se a cena atual CONTINUA o mesmo assunto da cena anterior, deixe "transition_text" como string vazia - não force transição onde não precisa.
  IMPORTANTE: o transition_text vai ser colocado ANTES do texto original da cena, sem nenhuma edição no texto da cena. Por isso: (1) SEMPRE termine o transition_text com ponto final, interrogação ou exclamação - NUNCA com vírgula ou travessão, porque a cena original começa com letra maiúscula e não pode ser alterada (isso já causou bugs reais, tipo "galileu Galilei" com nome próprio em minúscula); (2) NUNCA repita um nome próprio, data ou fato que a cena já menciona logo na primeira frase dela - se a cena já começa com "Giordano Bruno foi...", seu transition_text não pode também citar "Giordano Bruno", ou o nome vai aparecer duas vezes seguidas; (3) NUNCA escreva uma pergunta ou frase que a própria cena já responde na sequência.

Também decida para o vídeo inteiro:
- "music_mood": o clima musical geral, exatamente uma destas três opções: "suspense" (sombrio, mistério, crime, terror), "business" (finanças, negócios) ou "ambient" (neutro, qualquer outro clima).
- "use_fade_transitions": true se o conteúdo pede um corte mais dramático com fade entre cenas, false se cortes diretos combinam melhor com o ritmo do vídeo.%s

Narração por cena, em ordem (JSON):
%s

Retorne estritamente um JSON no formato {"music_mood":"suspense|business|ambient","use_fade_transitions":true,"scenes":[{"index":1,"image_prompt":"...","transition_text":""}]} com TODOS os índices presentes.`, niche, feedbackNote, string(briefsJSON))

	jsonStr, err := CallLLM(channelID, videoID, prompt)
	if err != nil {
		return nil, "", err
	}

	var resp directorNotes
	if err := json.Unmarshal([]byte(stripJSONFences(jsonStr)), &resp); err != nil {
		return nil, "", fmt.Errorf("failed to parse director response: %w", err)
	}
	if len(resp.Scenes) == 0 {
		return nil, "", fmt.Errorf("director response has no scenes")
	}
	if resp.MusicMood != "suspense" && resp.MusicMood != "business" && resp.MusicMood != "ambient" {
		resp.MusicMood = ""
	}

	respJSON, _ := json.Marshal(resp)
	return &resp, string(respJSON), nil
}

// ---------------------------------------------------------------------------
// Sonoplastia (Sound Design) - confirms/adjusts the Diretor's music mood
// choice and picks the background music mix level relative to narration.
// ---------------------------------------------------------------------------

type soundDesignNotes struct {
	MusicMood     string  `json:"music_mood"`      // "suspense", "business", or "ambient"
	MusicMixLevel float64 `json:"music_mix_level"` // 0.05-0.25, background music volume relative to narration
}

func applySoundDesign(channelID, videoID string, script *VideoScriptResult, niche, directorMood string) *soundDesignNotes {
	feedback := ""
	for attempt := 1; attempt <= 2; attempt++ {
		notes, notesJSON, err := runSoundDesignPass(channelID, videoID, script, niche, directorMood, feedback)
		if err != nil {
			log.Printf("Sonoplastia attempt %d failed, keeping default mix: %v", attempt, err)
			return nil
		}

		verdict := runSupervisorCheck(channelID, videoID, "sonoplastia",
			"music_mood deve ser exatamente \"suspense\", \"business\" ou \"ambient\" e coerente com o nicho/clima do vídeo. music_mix_level deve estar entre 0.05 e 0.25 (a música de fundo nunca pode competir com a narração).",
			notesJSON)
		if verdict.Approved || attempt == 2 {
			if !verdict.Approved {
				log.Printf("Sonoplastia: supervisor feedback not resolved after retry, using latest version anyway: %s", verdict.Feedback)
			}
			return notes
		}
		feedback = verdict.Feedback
	}
	return nil
}

func runSoundDesignPass(channelID, videoID string, script *VideoScriptResult, niche, directorMood, priorFeedback string) (*soundDesignNotes, string, error) {
	feedbackNote := ""
	if priorFeedback != "" {
		feedbackNote = "\n\nFeedback do supervisor na tentativa anterior, aplique antes de responder: " + priorFeedback
	}
	directorNote := ""
	if directorMood != "" {
		directorNote = fmt.Sprintf("\n\nO diretor de cinema sugeriu o clima musical \"%s\" - use essa sugestão a menos que o roteiro abaixo indique claramente um clima diferente.", directorMood)
	}

	prompt := fmt.Sprintf(`Você é um técnico de sonoplastia especializado em trilha sonora para vídeos documentais/dark de YouTube, no nicho '%s'.

Título: %s
Resumo do roteiro: %s%s%s

Decida:
- "music_mood": clima musical geral do vídeo, exatamente uma destas três opções: "suspense" (sombrio, mistério, crime, terror), "business" (finanças, negócios) ou "ambient" (neutro, qualquer outro clima). NUNCA escolha um clima alegre/animado para conteúdo sombrio ou sobre crimes reais.
- "music_mix_level": volume da música de fundo relativo à narração, um número entre 0.05 e 0.25 (a narração deve sempre ser claramente o elemento principal).

Retorne estritamente um JSON: {"music_mood":"suspense|business|ambient","music_mix_level":0.12}`,
		niche, script.Title, script.Description, directorNote, feedbackNote)

	jsonStr, err := CallLLM(channelID, videoID, prompt)
	if err != nil {
		return nil, "", err
	}

	var resp soundDesignNotes
	if err := json.Unmarshal([]byte(stripJSONFences(jsonStr)), &resp); err != nil {
		return nil, "", fmt.Errorf("failed to parse sound design response: %w", err)
	}
	if resp.MusicMood != "suspense" && resp.MusicMood != "business" && resp.MusicMood != "ambient" {
		return nil, "", fmt.Errorf("sound design response has invalid music_mood %q", resp.MusicMood)
	}
	if resp.MusicMixLevel < 0.05 || resp.MusicMixLevel > 0.25 {
		resp.MusicMixLevel = 0.12
	}

	respJSON, _ := json.Marshal(resp)
	return &resp, string(respJSON), nil
}

// ---------------------------------------------------------------------------
// Especialista em YouTube e Tendências - refines title/description/tags and
// crafts the thumbnail prompt for maximum CTR, with full knowledge of the
// finished script (the original title/description are written before the
// full narrative exists).
// ---------------------------------------------------------------------------

type youtubeOptimization struct {
	Title           string `json:"title"`
	Description     string `json:"description"`
	Tags            string `json:"tags"`
	ThumbnailPrompt string `json:"thumbnail_prompt"`
}

func optimizeForYouTube(channelID, videoID string, script *VideoScriptResult, niche string) *youtubeOptimization {
	feedback := ""
	for attempt := 1; attempt <= 2; attempt++ {
		yt, ytJSON, err := runYouTubeOptimizationPass(channelID, videoID, script, niche, feedback)
		if err != nil {
			log.Printf("Especialista em YouTube attempt %d failed, keeping original title/tags: %v", attempt, err)
			return nil
		}

		verdict := runSupervisorCheck(channelID, videoID, "otimização para YouTube",
			"O título deve ser magnético mas fiel ao conteúdo real do roteiro (sem clickbait enganoso), a descrição deve ser otimizada para SEO, as tags devem ser relevantes e separadas por vírgula, e o thumbnail_prompt deve estar em inglês, sem texto na imagem.",
			ytJSON)
		if verdict.Approved || attempt == 2 {
			if !verdict.Approved {
				log.Printf("Especialista em YouTube: supervisor feedback not resolved after retry, using latest version anyway: %s", verdict.Feedback)
			}
			return yt
		}
		feedback = verdict.Feedback
	}
	return nil
}

func runYouTubeOptimizationPass(channelID, videoID string, script *VideoScriptResult, niche, priorFeedback string) (*youtubeOptimization, string, error) {
	feedbackNote := ""
	if priorFeedback != "" {
		feedbackNote = "\n\nFeedback do supervisor na tentativa anterior, aplique antes de responder: " + priorFeedback
	}

	prompt := fmt.Sprintf(`Você é um especialista em YouTube e tendências, focado em CTR (taxa de cliques), SEO e algoritmo, no nicho '%s'.

Com base no roteiro completo abaixo, refine o título, a descrição e as tags (esses três campos em português).

Depois, escreva o campo "thumbnail_prompt" INTEIRAMENTE EM INGLÊS (é um prompt para um gerador de imagem por IA, que funciona melhor em inglês) - composição 4k de alto CTR, cores vibrantes, sem nenhum texto na imagem. NÃO escreva "thumbnail_prompt" em português sob nenhuma circunstância.%s

Título atual: %s
Descrição atual: %s
Tags atuais: %s
Roteiro completo: %s

Retorne estritamente um JSON: {"title":"...","description":"...","tags":"tag1, tag2, tag3","thumbnail_prompt":"English-only prompt here..."}`,
		niche, feedbackNote, script.Title, script.Description, script.Tags, script.Script)

	jsonStr, err := CallLLM(channelID, videoID, prompt)
	if err != nil {
		return nil, "", err
	}

	var resp youtubeOptimization
	if err := json.Unmarshal([]byte(stripJSONFences(jsonStr)), &resp); err != nil {
		return nil, "", fmt.Errorf("failed to parse youtube optimization response: %w", err)
	}
	if strings.TrimSpace(resp.Title) == "" {
		return nil, "", fmt.Errorf("youtube optimization response missing title")
	}

	respJSON, _ := json.Marshal(resp)
	return &resp, string(respJSON), nil
}
