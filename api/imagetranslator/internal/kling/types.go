// Package kling holds the wire types and client for Alibaba DashScope's
// Kling image-generation API (Beijing region only). Field names/enums are
// confirmed against the vendor's published API reference
// (help.aliyun.com/zh/model-studio/kling-image-generation-api-reference):
// POST .../services/aigc/image-generation/generation (create, requires
// X-DashScope-Async: enable), GET .../tasks/{task_id} (poll).
//
// v1 scope: only kling/kling-v3-image-generation is registered (single
// optional reference image via content[].image, n 1-9, aspect_ratio/
// resolution as documented for that model). kling/kling-v3-omni-image-
// generation (result_type/series_amount/4k/multi-image input) is
// deliberately out of scope for v1 — result_type and series_amount
// therefore have no field on CreateParameters at all, so there is nothing
// to pass through unmapped.
package kling

// Task status values reported in output.task_status. CANCELED and UNKNOWN
// are real, documented values (not just PENDING/RUNNING/SUCCEEDED/FAILED) —
// UNKNOWN in particular is what a query returns once a task_id ages past its
// 24-hour validity window, and does so persistently from then on.
const (
	TaskStatusPending   = "PENDING"
	TaskStatusRunning   = "RUNNING"
	TaskStatusSucceeded = "SUCCEEDED"
	TaskStatusFailed    = "FAILED"
	TaskStatusCanceled  = "CANCELED"
	TaskStatusUnknown   = "UNKNOWN"
)

// CreateRequest is the body for POST .../image-generation/generation.
type CreateRequest struct {
	Model      string           `json:"model"`
	Input      CreateInput      `json:"input"`
	Parameters CreateParameters `json:"parameters,omitempty"`
}

// CreateInput carries the single-turn messages array. Only one message is
// currently supported by the vendor (role "user"), holding exactly one text
// content item and zero-or-more image reference content items.
type CreateInput struct {
	Messages []Message `json:"messages"`
}

// Message is one element of CreateInput.Messages. Role is always "user" for
// v1 (the vendor documents it as optional but fixed to "user").
type Message struct {
	Role    string        `json:"role,omitempty"`
	Content []ContentItem `json:"content"`
}

// ContentItem is one element of a message's content array — either the
// (exactly one, required) text prompt, or a reference image. Text and Image
// are pointers so an empty string is distinguishable from "field absent":
// the vendor's own wire shape never sets both on the same content item.
type ContentItem struct {
	Text  *string `json:"text,omitempty"`
	Image *string `json:"image,omitempty"`
}

// CreateParameters carries the requested count/aspect-ratio/resolution/
// watermark. No ResultType/SeriesAmount fields exist here — v1 only
// registers kling/kling-v3-image-generation, which doesn't support them.
type CreateParameters struct {
	N           int64  `json:"n,omitempty"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
	// Watermark has no omitempty: the vendor's own default (false) IS the
	// value most requests want, and Go's zero value already matches it, but
	// omitting the tag makes an explicit false read as an intentionally-set
	// field rather than an accidentally-unset one — mirrors the identical
	// reasoning in the existing dashscope package for the same field.
	Watermark bool `json:"watermark"`
}

// CreateResponse is the response to a create-task call.
type CreateResponse struct {
	RequestID string       `json:"request_id"`
	Output    CreateOutput `json:"output"`
}

// CreateOutput is the output block of a create-task response.
type CreateOutput struct {
	TaskID     string `json:"task_id"`
	TaskStatus string `json:"task_status"`
}

// GetTaskResponse is the response to GET .../tasks/{task_id}. Usage is a
// top-level sibling of Output (per the vendor's documented shape), not
// nested inside it. Code/Message are populated only on the flat,
// no-output-wrapper query-time failure shape (the vendor's own verbatim
// "Task-level failure response" example — {request_id, code, message}, no
// output wrapper, no task_id, no task_status at all) — a DIFFERENT, flatter
// shape than a normal terminal FAILED status (which nests code/message
// inside Output instead).
type GetTaskResponse struct {
	RequestID string     `json:"request_id"`
	Output    TaskOutput `json:"output"`
	Usage     *TaskUsage `json:"usage,omitempty"`
	Code      string     `json:"code,omitempty"`
	Message   string     `json:"message,omitempty"`
}

// TaskOutput is the output block of a get-task response. Choices always
// contains exactly one element on a well-formed response (the vendor's own
// documented convention, "此数组仅包含一个元素") — but that is a convention
// for well-formed output, not a structural guarantee this code can rely on
// for an adversarial or malformed response; see
// translate.FromKlingGetTaskResponse's explicit empty-Choices guard before
// any Choices[0] indexing. Code/Message here are populated by the vendor
// only when TaskStatus is FAILED (nested-in-output shape) — this has not
// been directly evidenced live for Kling (only for Vidu, which shares the
// same underlying transport), so the client also handles the flat,
// no-output-wrapper shape at GetTaskResponse's own top level (above).
type TaskOutput struct {
	TaskID        string   `json:"task_id"`
	TaskStatus    string   `json:"task_status"`
	Finished      bool     `json:"finished,omitempty"`
	Choices       []Choice `json:"choices,omitempty"`
	Code          string   `json:"code,omitempty"`
	Message       string   `json:"message,omitempty"`
	SubmitTime    string   `json:"submit_time,omitempty"`
	ScheduledTime string   `json:"scheduled_time,omitempty"`
	EndTime       string   `json:"end_time,omitempty"`
}

// Choice is the single element of TaskOutput.Choices on a well-formed
// response.
type Choice struct {
	FinishReason string        `json:"finish_reason,omitempty"`
	Message      ChoiceMessage `json:"message"`
}

// ChoiceMessage carries the generated images — one content entry per
// generated image (N entries where N == the requested n).
type ChoiceMessage struct {
	Role    string             `json:"role,omitempty"`
	Content []ImageContentItem `json:"content"`
}

// ImageContentItem is one generated image. Type is always the fixed value
// "image". Image is the generated image's URL (PNG), valid for 30 days per
// the vendor's docs — the sidecar downloads and base64-encodes it before
// this validity window matters to a client.
type ImageContentItem struct {
	Type  string `json:"type,omitempty"`
	Image string `json:"image,omitempty"`
}

// TaskUsage is the actual-output usage the vendor reports once a task
// completes. Per the vendor's own docs, usage "只对成功的结果计数" — only
// successful results are counted — which is exactly why ImageCount can be
// less than the originally-requested n on a nominally SUCCEEDED task (a
// vendor-side partial-success case the translate layer checks explicitly,
// see FromKlingGetTaskResponse). Size/SR are vendor-native resolution
// formats ("1024*1024" / "1080"), informational only for Kling (billing
// keys on image count, not resolution).
type TaskUsage struct {
	ImageCount int64  `json:"image_count,omitempty"`
	Size       string `json:"size,omitempty"`
	SR         string `json:"SR,omitempty"`
}
