package eval

// ── Seed corpus (ported from OmniRoute eval/corpus.ts) ─────────────────────
// A small built-in corpus covering each content kind. Real deployments can
// extend this with captured (anonymized) cases. Each case has a context, a
// question, and an optional gold answer for accuracy grading.

// SeedCorpus returns the built-in evaluation corpus.
func SeedCorpus() []EvalCase {
	return []EvalCase{
		{
			ID:       "prose-001",
			Kind:     KindProse,
			Context:  "Hello there! I would like you to please help me understand how photosynthesis works. Basically, I just want to essentially know the core process. Thank you so much for your help! I really appreciate it. Could you please explain in detail how plants convert sunlight into energy? It would be great if you could also mention the role of chlorophyll and the overall chemical equation involved.",
			Question: "Summarize how photosynthesis works in one sentence.",
			Gold:     "Photosynthesis is the process by which plants use chlorophyll to convert sunlight, water, and carbon dioxide into glucose and oxygen.",
		},
		{
			ID:       "code-001",
			Kind:     KindCode,
			Context:  "Here is my code:\n```go\nfunc fib(n int) int {\n\tif n <= 1 {\n\t\treturn n\n\t}\n\treturn fib(n-1) + fib(n-2)\n}\n```\nI am trying to understand the time complexity of this function. I have been told it is exponential but I would like you to please confirm and explain why that is the case. Thank you!",
			Question: "What is the time complexity of this function and why?",
			Gold:     "The time complexity is O(2^n) exponential because each call branches into two recursive calls, creating a binary tree of depth n.",
		},
		{
			ID:       "logs-001",
			Kind:     KindLogs,
			Context:  "2024-01-15T10:23:01Z INFO server started on :8080\n2024-01-15T10:23:02Z INFO connected to database\n2024-01-15T10:23:05Z WARN high memory usage detected: 85%\n2024-01-15T10:23:10Z ERROR connection timeout to upstream service api.example.com after 30s\n2024-01-15T10:23:11Z INFO retrying connection attempt 1/3\n2024-01-15T10:23:15Z INFO connection restored\n2024-01-15T10:24:00Z INFO health check passed",
			Question: "What error occurred and was it resolved?",
			Gold:     "A connection timeout to api.example.com occurred after 30 seconds, and it was resolved after one retry attempt.",
		},
		{
			ID:       "tool-output-json-001",
			Kind:     KindToolOutputJSON,
			Context:  "The API returned:\n{\"status\":200,\"data\":{\"users\":[{\"id\":1,\"name\":\"Alice\",\"email\":\"alice@example.com\",\"active\":true},{\"id\":2,\"name\":\"Bob\",\"email\":\"bob@example.com\",\"active\":false},{\"id\":3,\"name\":\"Charlie\",\"email\":\"charlie@example.com\",\"active\":true}],\"total\":3,\"page\":1,\"per_page\":10}}",
			Question: "How many active users are there?",
			Gold:     "There are 2 active users: Alice and Charlie.",
		},
		{
			ID:       "multi-turn-001",
			Kind:     KindMultiTurn,
			Context:  "User: What is the capital of France?\nAssistant: The capital of France is Paris.\nUser: What is the population of that city?\nAssistant: Paris has a population of approximately 2.1 million people in the city proper.\nUser: And what river runs through it?\nAssistant: The Seine river runs through Paris.\nUser: Thanks! Now, going back to the population question — can you also tell me the metro area population?",
			Question: "What is the metro area population of Paris?",
			Gold:     "The Paris metro area has a population of approximately 12-13 million people.",
		},
	}
}
