package deepseek

type wireContentPart interface{ contentKind() string }
type wireTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (wireTextPart) contentKind() string { return "text" }

type wireImageURL struct {
	URL string `json:"url"`
}
type wireImagePart struct {
	Type     string       `json:"type"`
	ImageURL wireImageURL `json:"image_url"`
}

func (wireImagePart) contentKind() string { return "image_url" }

type wireVisualMessage struct {
	Role    string            `json:"role"`
	Content []wireContentPart `json:"content"`
}

func (wireVisualMessage) wireRole() string { return "user" }
