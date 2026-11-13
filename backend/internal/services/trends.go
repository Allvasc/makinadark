package services

import (
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type RSSFeed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Items []RSSItem `xml:"item"`
	} `xml:"channel"`
}

type RSSItem struct {
	Title       string `xml:"title"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

// FetchRealTimeTrends fetches live search trends from Google Trends RSS.
// channelID may be empty (e.g. the standalone /api/trends explorer endpoint
// isn't tied to a channel) - cost logging just no-ops in that case.
func FetchRealTimeTrends(channelID, niche string) ([]string, error) {
	// Try fetching Google Trends RSS for Brazil and US
	trendsBR, err1 := fetchTrendsFromRSS("https://trends.google.com/trending/rss?geo=BR")
	trendsUS, err2 := fetchTrendsFromRSS("https://trends.google.com/trending/rss?geo=US")

	var allTrends []string
	if err1 == nil {
		allTrends = append(allTrends, trendsBR...)
	}
	if err2 == nil {
		allTrends = append(allTrends, trendsUS...)
	}

	if len(allTrends) == 0 {
		log.Println("Google Trends RSS unavailable. Using AI fallback trend discovery.")
		return fetchAITrendingTopics(channelID, niche)
	}

	// Use LLM to filter and map trending search keywords to the channel's niche
	return mapTrendsToNiche(channelID, allTrends, niche)
}

func fetchTrendsFromRSS(rssURL string) ([]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(rssURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rss status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var feed RSSFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}

	var keywords []string
	for _, item := range feed.Channel.Items {
		if item.Title != "" {
			keywords = append(keywords, item.Title)
		}
	}

	return keywords, nil
}

func mapTrendsToNiche(channelID string, rawTrends []string, niche string) ([]string, error) {
	trendsText := strings.Join(rawTrends[:min(len(rawTrends), 20)], ", ")

	prompt := fmt.Sprintf(`Aqui estão os assuntos mais pesquisados e em pico de tendência no Google Trends hoje:
[%s]

Você é um estrategista sênior de canais Dark no YouTube.
Sua missão é selecionar os assuntos e fazer a ponte temática perfeita para o nicho de '%s'.

REGRAS OBRIGATÓRIAS (FATOS REAIS E HISTÓRIAS CONCRETAS):
1. FOCO EM HISTÓRIAS E FATOS REAIS: Os tópicos DEVEM se referir a histórias reais, acontecimentos verídicos, casos documentados, desfechos históricos ou fatos concretos. NUNCA gere tópicos filosóficos, existenciais ou frases genéricas vazias.
2. Crie 3 ideias de títulos/tópicos virais de altíssima taxa de clique (CTR) baseados em fatos e histórias reais no nicho de '%s'.

Retorne estritamente os 3 tópicos separados por vírgula em texto simples, sem numeração.`, trendsText, niche, niche)

	resultStr, err := CallLLMText(channelID, "", prompt)
	if err != nil {
		return fetchAITrendingTopics(channelID, niche)
	}

	topics := strings.Split(resultStr, ",")
	var cleaned []string
	for _, t := range topics {
		trimmed := strings.TrimSpace(t)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}

	if len(cleaned) == 0 {
		return fetchAITrendingTopics(channelID, niche)
	}

	return cleaned, nil
}

func fetchAITrendingTopics(channelID, niche string) ([]string, error) {
	prompt := fmt.Sprintf(`Identifique 3 histórias reais, fatos históricos surpreendentes ou casos documentados de altíssimo interesse no nicho de '%s'.
REGRA ABSOLUTA: Os tópicos DEVEM ser focados em HISTÓRIAS REAIS E FATOS CONCRETOS. É proibido gerar temas filosóficos, abstratos ou existenciais.
Retorne os 3 tópicos separados por vírgula em texto simples, sem numeração.`, niche)

	resultStr, err := CallLLMText(channelID, "", prompt)
	if err != nil {
		return []string{
			fmt.Sprintf("A História Real Por Trás dos Maiores Casos de %s", niche),
			fmt.Sprintf("O Evento Histórico em %s Que Mudou o Mundo", niche),
			fmt.Sprintf("Descobertas Fatuais Impressionantes no Nicho de %s", niche),
		}, nil
	}

	topics := strings.Split(resultStr, ",")
	var cleaned []string
	for _, t := range topics {
		trimmed := strings.TrimSpace(t)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}

	return cleaned, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
