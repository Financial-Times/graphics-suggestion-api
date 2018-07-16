package main

type trainingItem struct {
	text     string
	concepts []string
}

type annotation struct {
	Predicate string `json:"predicate"`
	Id        string `json:"id"`
}

type Concept struct {
	ID         string `json:"id"`
	APIURL     string `json:"apiUrl,omitempty"`
	Type       string `json:"type,omitempty"`
	PrefLabel  string `json:"prefLabel,omitempty"`
	IsFTAuthor *bool  `json:"isFTAuthor,omitempty"`
	Predicate  string `json:"predicate,omitempty"`
}

type SuggestionsResponse struct {
	Suggestions []Concept `json:"suggestions"`
}

type InternalConcordancesResponse struct {
	Concepts map[string]Concept `json:"concepts"`
}
