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

type Model struct {
	Name  string
	Price float64
}

func CheaperModel(current string, currentPrice float64, candidates []Model) string {
	if currentPrice <= 0 {
		return current
	}
	bestName, bestPrice := "", 0.0
	for _, m := range candidates {
		if m.Price <= 0 || m.Name == current {
			continue
		}
		if bestName == "" || m.Price < bestPrice {
			bestName, bestPrice = m.Name, m.Price
		}
	}
	if bestName != "" && bestPrice < currentPrice {
		return bestName
	}
	return current
}
