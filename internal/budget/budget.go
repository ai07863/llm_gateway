package budget

type Decision int

const (
	Allow Decision = iota
	Degrade
	Reject
)

func Evaluate(monthlyCost, limit, softRatio float64) Decision {
	if limit <= 0 {
		return Allow
	}
	soft := limit * softRatio
	switch {
	case monthlyCost >= limit:
		return Reject
	case monthlyCost >= soft:
		return Degrade
	default:
		return Allow
	}
}

var cheaper = map[string]string{
	"gpt-4o":        "deepseek-chat",
	"gpt-4o-mini":   "deepseek-chat",
	"deepseek-chat": "deepseek-chat",
}

func CheaperModel(model string) string {
	if m, ok := cheaper[model]; ok {
		return m
	}
	return model
}
