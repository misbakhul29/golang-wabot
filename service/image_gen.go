package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

var ChatMap sync.Map
var WAClient *whatsmeow.Client

type ArticleData struct {
	ID string `json:"id"`
}

type Payload struct {
	Prompt      string      `json:"prompt"`
	AspectRatio string      `json:"aspectRatio"`
	Style       string      `json:"style"`
	WebhookURL  string      `json:"webhookUrl"`
	ArticleData ArticleData `json:"articleData,omitempty"`
}

func RequestImageGeneration(chat types.JID, userInput string) {
	prompt := ""
	aspectRatio := "16:9"
	style := "cinematic"

	if strings.HasPrefix(userInput, "{") && strings.HasSuffix(userInput, "}") {
		parts := strings.Split(strings.Trim(userInput, "{}"), ",")
		if len(parts) > 0 {
			prompt = strings.TrimSpace(parts[0])
		}
		if len(parts) > 1 {
			aspectRatio = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			style = strings.TrimSpace(parts[2])
		}
	} else {
		prompt = userInput
	}

	fmt.Printf("Sending request for prompt: \"%s\", Ratio: %s, Style: %s\n", prompt, aspectRatio, style)

	id := fmt.Sprintf("wabot-%d", time.Now().Unix())
	ChatMap.Store(id, chat)

	webhookURL := fmt.Sprintf("%s/webhook/image", os.Getenv("WEBHOOK_HOSTNAME"))
	if strings.Contains(webhookURL, "?") {
		webhookURL += "&id=" + id
	} else {
		webhookURL += "?id=" + id
	}

	payload := Payload{
		Prompt:      prompt,
		AspectRatio: aspectRatio,
		Style:       style,
		WebhookURL:  webhookURL,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("❌ Error marshaling JSON: %v\n", err)
		return
	}

	apiURL := os.Getenv("IMAGE_GEN_URL")
	apiToken := os.Getenv("IMAGE_GEN_TOKEN")

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Printf("❌ Error creating request: %v\n", err)
		return
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ Fetch error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("❌ HTTP error! status: %d, body: %s\n", resp.StatusCode, string(body))
		if WAClient != nil {
			errorMsg := fmt.Sprintf("❌ API Error: %d", resp.StatusCode)
			switch resp.StatusCode {
			case 401:
				errorMsg += " (Unauthorized - Token expired)"
			case 422:
				errorMsg += fmt.Sprintf(" (Unprocessable Entity - Details: %s)", string(body))
			case 500:
				errorMsg += " (Internal Server Error)"
			default:
				errorMsg += fmt.Sprintf(" (Unknown Error - Details: %s)", string(body))
			}
			_, _ = WAClient.SendMessage(context.Background(), chat, &waE2E.Message{
				Conversation: proto.String(errorMsg),
			})
		}
	} else {
		var data interface{}
		if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
			fmt.Printf("✅ Request Success! %v\n", data)
		} else {
			fmt.Println("✅ Request Success! (No body)")
		}
	}
}

func HandleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	contentType := r.Header.Get("Content-Type")
	fmt.Printf("📥 Received webhook request: %s %s (Content-Type: %s)\n", r.Method, r.URL.Path, contentType)

	requestID := r.URL.Query().Get("id")
	if requestID == "" {
		requestID = r.URL.Query().Get("request_id")
	}

	var imageBuffer []byte

	if strings.Contains(contentType, "application/json") {
		var payload struct {
			ID          string      `json:"id"`
			RequestID   string      `json:"request_id"`
			ImageBuffer []byte      `json:"imageBuffer"`
			ImageUrl    string      `json:"imageUrl"`
			ArticleData ArticleData `json:"articleData"`
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			fmt.Printf("❌ Error reading webhook body: %v\n", err)
			http.Error(w, "Failed to read body", http.StatusInternalServerError)
			return
		}

		if err := json.Unmarshal(body, &payload); err != nil {
			fmt.Printf("❌ Error unmarshaling webhook JSON: %v\n", err)
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		if requestID == "" {
			if payload.ID != "" {
				requestID = payload.ID
			} else if payload.RequestID != "" {
				requestID = payload.RequestID
			} else {
				requestID = payload.ArticleData.ID
			}
		}
		if len(payload.ImageBuffer) > 0 {
			imageBuffer = payload.ImageBuffer
		} else if payload.ImageUrl != "" {
			fmt.Printf("🌐 Downloading image from URL: %s\n", payload.ImageUrl)
			resp, err := http.Get(payload.ImageUrl)
			if err != nil {
				fmt.Printf("❌ Error downloading image: %v\n", err)
				http.Error(w, "Failed to download image", http.StatusInternalServerError)
				return
			}
			defer resp.Body.Close()
			imageBuffer, err = io.ReadAll(resp.Body)
			if err != nil {
				fmt.Printf("❌ Error reading downloaded image: %v\n", err)
				http.Error(w, "Failed to read image", http.StatusInternalServerError)
				return
			}
		}
	} else if strings.HasPrefix(contentType, "image/") || strings.Contains(contentType, "octet-stream") {
		// Handle raw binary image
		if requestID == "" {
			requestID = r.Header.Get("X-Request-ID")
		}

		var err error
		imageBuffer, err = io.ReadAll(r.Body)
		if err != nil {
			fmt.Printf("❌ Error reading raw binary body: %v\n", err)
			http.Error(w, "Failed to read binary data", http.StatusInternalServerError)
			return
		}
	} else if strings.Contains(contentType, "multipart/form-data") {
		err := r.ParseMultipartForm(10 << 20) // 10MB limit
		if err != nil {
			fmt.Printf("❌ Error parsing multipart form: %v\n", err)
			http.Error(w, "Failed to parse form", http.StatusInternalServerError)
			return
		}

		if requestID == "" {
			requestID = r.FormValue("id")
		}
		if requestID == "" {
			requestID = r.FormValue("request_id")
		}
		if requestID == "" {
			adJSON := r.FormValue("articleData")
			if adJSON != "" {
				var ad ArticleData
				if err := json.Unmarshal([]byte(adJSON), &ad); err == nil {
					requestID = ad.ID
				}
			}
		}

		file, _, err := r.FormFile("image")
		if err != nil {
			file, _, err = r.FormFile("file")
		}
		if err == nil {
			defer file.Close()
			imageBuffer, err = io.ReadAll(file)
			if err != nil {
				fmt.Printf("❌ Error reading multipart file: %v\n", err)
				http.Error(w, "Failed to read file", http.StatusInternalServerError)
				return
			}
		} else {
			// Check if buffer or URL was sent as a form field
			if bufStr := r.FormValue("imageBuffer"); bufStr != "" {
				imageBuffer = []byte(bufStr)
			} else if urlStr := r.FormValue("imageUrl"); urlStr != "" {
				fmt.Printf("🌐 Downloading image from URL (form): %s\n", urlStr)
				resp, err := http.Get(urlStr)
				if err == nil {
					defer resp.Body.Close()
					imageBuffer, _ = io.ReadAll(resp.Body)
				}
			}
		}
	} else {
		fmt.Printf("⚠️ Unsupported Content-Type: %s\n", contentType)
		http.Error(w, "Unsupported Content-Type", http.StatusUnsupportedMediaType)
		return
	}

	if requestID == "" {
		fmt.Println("⚠️ Webhook received without request ID (no ID in JSON or headers/query)")
		http.Error(w, "Missing ID", http.StatusBadRequest)
		return
	}

	chatJID, ok := ChatMap.Load(requestID)
	if !ok {
		fmt.Printf("⚠️ No chat JID found for request ID: %s (maybe it expired)\n", requestID)
		w.WriteHeader(http.StatusOK) // API should stop retrying
		return
	}

	if len(imageBuffer) == 0 {
		fmt.Println("⚠️ Webhook received with no image data")
		http.Error(w, "No image data", http.StatusBadRequest)
		return
	}

	jid := chatJID.(types.JID)
	if WAClient != nil {
		fmt.Printf("🚀 Sending image back to %s (Length: %d bytes)\n", jid.User, len(imageBuffer))

		resp, err := WAClient.Upload(context.Background(), imageBuffer, whatsmeow.MediaImage)
		if err != nil {
			fmt.Printf("❌ Error uploading image to WA: %v\n", err)
			http.Error(w, "Failed to upload to WA", http.StatusInternalServerError)
			return
		}

		_, err = WAClient.SendMessage(context.Background(), jid, &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				Caption:       proto.String("Generated Image"),
				Mimetype:      proto.String("image/png"),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			},
		})
		if err != nil {
			fmt.Printf("❌ Error sending image message: %v\n", err)
		} else {
			fmt.Println("✅ Image sent successfully!")
			ChatMap.Delete(requestID)
		}
	} else {
		fmt.Println("❌ WAClient is not initialized")
		http.Error(w, "WAClient not ready", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
}
