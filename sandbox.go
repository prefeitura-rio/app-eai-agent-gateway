// main.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/oauth2/google"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

func getAccessToken(ctx context.Context) (string, error) {
	ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return "", err
	}
	tok, err := ts.Token()
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

func postQuery(ctx context.Context, accessToken, project, location, engineID string, payload map[string]interface{}) (map[string]interface{}, error) {
	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/reasoningEngines/%s:query",
		location, project, location, engineID)

	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("non-2xx: %d - %s", resp.StatusCode, string(bodyBytes))
	}
	var out map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w (raw: %s)", err, string(bodyBytes))
	}
	return out, nil
}

func pollOperation(ctx context.Context, accessToken, location, operationName string, interval time.Duration, timeout time.Duration) (map[string]interface{}, error) {
	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/%s", location, operationName)
	client := http.DefaultClient
	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("polling timed out after %s", timeout)
		}

		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("poll non-2xx: %d - %s", resp.StatusCode, string(bodyBytes))
		}

		var op map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &op); err != nil {
			return nil, err
		}

		if done, _ := op["done"].(bool); done {
			return op, nil
		}

		time.Sleep(interval)
	}
}

func extractOperationName(resp map[string]interface{}) string {
	if n, ok := resp["name"].(string); ok && n != "" {
		return n
	}
	if op, ok := resp["operation"].(map[string]interface{}); ok {
		if n, ok := op["name"].(string); ok && n != "" {
			return n
		}
	}
	return ""
}

func main() {
	ctx := context.Background()

	project := flag.String("project", "", "GCP project ID")
	location := flag.String("location", "us-central1", "Agent location (e.g. us-central1)")
	engine := flag.String("engine", "", "Reasoning Engine ID (the agent id)")
	message := flag.String("message", "do a long background job", "Message to send as human content")
	threadID := flag.String("thread", "default-thread", "thread_id to put in config.configurable.thread_id")
	pollInterval := flag.Int("poll", 5, "poll interval seconds for LRO")
	pollTimeout := flag.Int("timeout", 300, "max seconds to wait for operation to finish")
	flag.Parse()

	if *project == "" || *engine == "" {
		fmt.Fprintln(os.Stderr, "project and engine flags are required")
		flag.Usage()
		os.Exit(2)
	}

	accessToken, err := getAccessToken(ctx)
	must(err)

	// <<< IMPORTANT FIX: the top-level `input` must contain keys that are the *parameter names*
	// for the class method. Here async_query expects named params `input` and `config`,
	// so we set input.input = { messages: [...] } and input.config = { configurable: {...} }.
	payload := map[string]interface{}{
		"classMethod": "async_query",
		"input": map[string]interface{}{
			"input": map[string]interface{}{
				"messages": []map[string]interface{}{
					{"role": "human", "content": *message},
				},
			},
			"config": map[string]interface{}{ // this becomes the named param `config`
				"configurable": map[string]interface{}{
					"thread_id": *threadID,
				},
			},
		},
	}

	fmt.Println("Starting async_query call...")
	resp, err := postQuery(ctx, accessToken, *project, *location, *engine, payload)
	must(err)

	opName := extractOperationName(resp)
	if opName == "" {
		b, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println("No operation returned; response:\n", string(b))
		return
	}

	fmt.Println("Operation name:", opName)
	fmt.Println("Polling until done...")
	op, err := pollOperation(ctx, accessToken, *location, opName, time.Duration(*pollInterval)*time.Second, time.Duration(*pollTimeout)*time.Second)
	must(err)

	pretty, _ := json.MarshalIndent(op, "", "  ")
	fmt.Println("Operation finished. Full operation JSON:\n", string(pretty))

	if responseObj, ok := op["response"]; ok {
		prettyResp, _ := json.MarshalIndent(responseObj, "", "  ")
		fmt.Println("Final response:\n", string(prettyResp))
	} else if errObj, ok := op["error"]; ok {
		fmt.Println("Operation finished with error:", errObj)
	} else {
		fmt.Println("Operation finished but no 'response' or 'error' field found. Inspect full JSON above.")
	}
}
