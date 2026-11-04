package service

import (
	"math"
	"regexp"
	"strings"
	"sync"
	"time"
)

type TreeNode struct {
	ID       string    `json:"id" bson:"id"`
	Type     string    `json:"type" bson:"type"` // "decision" or "result"
	Field    string    `json:"field,omitempty" bson:"field,omitempty"`
	Operator string    `json:"operator,omitempty" bson:"operator,omitempty"`
	Value    any       `json:"value,omitempty" bson:"value,omitempty"`
	IsAdult  bool      `json:"isAdult,omitempty" bson:"isAdult,omitempty"`
	YesNode  *TreeNode `json:"yesNode,omitempty" bson:"yesNode,omitempty"`
	NoNode   *TreeNode `json:"noNode,omitempty" bson:"noNode,omitempty"`
}

type KeywordItem struct {
	ID   string `json:"id" bson:"id"`
	Name string `json:"name" bson:"name"`
}

type TraceStep struct {
	NodeID       string `json:"nodeId"`
	Type         string `json:"type"`
	Field        string `json:"field,omitempty"`
	Operator     string `json:"operator,omitempty"`
	Value        any    `json:"value,omitempty"`
	ConditionMet bool   `json:"conditionMet,omitempty"`
	IsAdult      bool   `json:"isAdult,omitempty"`
}

var defaultKeywords = []KeywordItem{
	{ID: "190370", Name: "erotic movie"},
	{ID: "207767", Name: "erotica"},
	{ID: "256466", Name: "erotic romance"},
	{ID: "302868", Name: "softcore"},
	{ID: "281741", Name: "sensual"},
	{ID: "738", Name: "sexuality"},
	{ID: "155477", Name: "softcore porn"},
	{ID: "170827", Name: "adult movie"},
	{ID: "14794", Name: "nudity"},
	{ID: "3182", Name: "sex"},
	{ID: "1326", Name: "infidelity"},
	{ID: "286925", Name: "taboo"},
	{ID: "11034", Name: "fetish"},
	{ID: "2804", Name: "passion"},
}

func GetDefaultKeywords() []KeywordItem {
	keywords := make([]KeywordItem, len(defaultKeywords))
	copy(keywords, defaultKeywords)
	return keywords
}

func GetDefaultTree() *TreeNode {
	return &TreeNode{
		ID:       "node_root",
		Type:     "decision",
		Field:    "adult_flag",
		Operator: "is_true",
		YesNode: &TreeNode{
			ID:      "res_adult_flag_true",
			Type:    "result",
			IsAdult: true,
		},
		NoNode: &TreeNode{
			ID:       "node_keywords",
			Type:     "decision",
			Field:    "adult_keywords",
			Operator: "is_true",
			YesNode: &TreeNode{
				ID:      "res_keywords_true",
				Type:    "result",
				IsAdult: true,
			},
			NoNode: &TreeNode{
				ID:       "node_age",
				Type:     "decision",
				Field:    "release_age_days",
				Operator: ">=",
				Value:    float64(60),
				YesNode: &TreeNode{
					ID:       "node_votes",
					Type:     "decision",
					Field:    "vote_count",
					Operator: "<",
					Value:    float64(30),
					YesNode: &TreeNode{
						ID:      "res_low_votes_old",
						Type:    "result",
						IsAdult: true,
					},
					NoNode: &TreeNode{
						ID:      "res_good_votes",
						Type:    "result",
						IsAdult: false,
					},
				},
				NoNode: &TreeNode{
					ID:      "res_new_release_safe",
					Type:    "result",
					IsAdult: false,
				},
			},
		},
	}
}

type AdultDecisionEngine struct {
	mu           sync.RWMutex
	tree         *TreeNode
	keywords     []KeywordItem
	keywordRegex *regexp.Regexp
}

func NewAdultDecisionEngine() *AdultDecisionEngine {
	e := &AdultDecisionEngine{
		tree:     GetDefaultTree(),
		keywords: defaultKeywords,
	}
	e.rebuildKeywordRegex()
	return e
}

func (e *AdultDecisionEngine) rebuildKeywordRegex() {
	defaultTerms := []string{"erotic", "erotica", "porn", "porno", "pornography", "softcore", "hentai", "nsfw", "xxx"}
	seen := make(map[string]bool)
	var terms []string

	for _, t := range defaultTerms {
		if !seen[t] {
			terms = append(terms, regexp.QuoteMeta(t))
			seen[t] = true
		}
	}

	for _, k := range e.keywords {
		trimmed := strings.ToLower(strings.TrimSpace(k.Name))
		if len(trimmed) >= 3 && !seen[trimmed] {
			terms = append(terms, regexp.QuoteMeta(trimmed))
			seen[trimmed] = true
		}
	}

	pattern := `(?i)\b(` + strings.Join(terms, "|") + `)\b`
	e.keywordRegex = regexp.MustCompile(pattern)
}

func (e *AdultDecisionEngine) Evaluate(item map[string]any) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.traverse(e.tree, item, nil)
}

func (e *AdultDecisionEngine) Test(tree *TreeNode, item map[string]any) (bool, []TraceStep) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	activeTree := tree
	if activeTree == nil {
		activeTree = e.tree
	}

	var trace []TraceStep
	isAdult := e.traverse(activeTree, item, &trace)
	return isAdult, trace
}

func (e *AdultDecisionEngine) traverse(node *TreeNode, item map[string]any, trace *[]TraceStep) bool {
	if node == nil {
		return false
	}

	if node.Type == "result" {
		if trace != nil {
			*trace = append(*trace, TraceStep{
				NodeID:  node.ID,
				Type:    "result",
				IsAdult: node.IsAdult,
			})
		}
		return node.IsAdult
	}

	conditionMet := e.evaluateCondition(node, item)
	if trace != nil {
		*trace = append(*trace, TraceStep{
			NodeID:       node.ID,
			Type:         "decision",
			Field:        node.Field,
			Operator:     node.Operator,
			Value:        node.Value,
			ConditionMet: conditionMet,
		})
	}

	if conditionMet {
		return e.traverse(node.YesNode, item, trace)
	}
	return e.traverse(node.NoNode, item, trace)
}

func (e *AdultDecisionEngine) evaluateCondition(node *TreeNode, item map[string]any) bool {
	field := node.Field
	op := node.Operator

	switch field {
	case "adult_flag":
		adult, _ := item["adult"].(bool)
		isAdultFlag := adult || item["_isAdultDiscover"] == true
		if op == "is_true" {
			return isAdultFlag
		}
		return !isAdultFlag

	case "adult_keywords":
		title, _ := item["title"].(string)
		if title == "" {
			title, _ = item["name"].(string)
		}
		overview, _ := item["overview"].(string)

		hasKw := e.keywordRegex.MatchString(title) || e.keywordRegex.MatchString(overview)
		if !hasKw {
			if genres, ok := item["genres"].([]any); ok {
				for _, g := range genres {
					name := ""
					if gStr, ok := g.(string); ok {
						name = gStr
					} else if gMap, ok := g.(map[string]any); ok {
						name, _ = gMap["name"].(string)
					}
					if e.keywordRegex.MatchString(name) {
						hasKw = true
						break
					}
				}
			}
		}
		if op == "is_true" {
			return hasKw
		}
		return !hasKw

	case "has_imdb":
		imdbID, _ := item["imdb_id"].(string)
		has := strings.TrimSpace(imdbID) != ""
		if op == "is_true" {
			return has
		}
		return !has

	case "is_indian":
		origLang, _ := item["original_language"].(string)
		indianLangs := map[string]bool{"hi": true, "ta": true, "te": true, "ml": true, "kn": true, "bn": true, "mr": true, "pa": true}
		isInd := indianLangs[origLang]
		if !isInd {
			if countries, ok := item["origin_country"].([]any); ok {
				for _, c := range countries {
					if cStr, ok := c.(string); ok && cStr == "IN" {
						isInd = true
						break
					}
				}
			}
		}
		if op == "is_true" {
			return isInd
		}
		return !isInd

	case "is_bollywood_endpoint":
		isBw := item["is_bollywood_endpoint"] == true || item["is_bollywood_feed"] == true
		if op == "is_true" {
			return isBw
		}
		return !isBw

	case "vote_count":
		votes := getFloat(item["vote_count"])
		target := getFloat(node.Value)
		return compareNumeric(votes, target, op)

	case "release_age_days":
		dateStr, _ := item["release_date"].(string)
		if dateStr == "" {
			dateStr, _ = item["first_air_date"].(string)
		}
		if dateStr == "" {
			return false
		}
		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			return false
		}
		diffDays := math.Floor(time.Since(t).Hours() / 24)
		if diffDays < 0 {
			diffDays = 0
		}
		target := getFloat(node.Value)
		return compareNumeric(diffDays, target, op)
	}

	return false
}

func getFloat(val any) float64 {
	switch v := val.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

func compareNumeric(a, b float64, op string) bool {
	switch op {
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	case "==":
		return a == b
	default:
		return false
	}
}

func (e *AdultDecisionEngine) GetTree() *TreeNode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.tree
}

func (e *AdultDecisionEngine) SetTree(tree *TreeNode) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tree = tree
}

func (e *AdultDecisionEngine) ResetTree() *TreeNode {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tree = GetDefaultTree()
	return e.tree
}

func (e *AdultDecisionEngine) GetKeywords() []KeywordItem {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.keywords
}

func (e *AdultDecisionEngine) SetKeywords(keywords []KeywordItem) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.keywords = keywords
	e.rebuildKeywordRegex()
}

func (e *AdultDecisionEngine) ResetKeywords() []KeywordItem {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.keywords = defaultKeywords
	e.rebuildKeywordRegex()
	return e.keywords
}
