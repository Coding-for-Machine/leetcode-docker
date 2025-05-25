package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/Coding-for-Machine/leetcode-docker/app"
	"github.com/fasthttp/websocket"
	"github.com/joho/godotenv"
	"github.com/valyala/fasthttp"
)

const (
	ALLOWED_ORIGINS = "*"
	ALLOWED_METHODS = "GET, POST, OPTIONS"
	ALLOWED_HEADERS = "Content-Type"
)

var upgrader = websocket.FastHTTPUpgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
		return true
	},
}

// CodeRequest - Problem ID bilan kodni bajarish uchun
type CodeRequest struct {
	ProblemID int    `json:"problem_id"` // Yangi: problem ID
	Language  string `json:"language"`
	Code      string `json:"code"`
	TimeoutMs int    `json:"timeout_ms"`
	MemoryMb  int    `json:"memory_mb"`
	CpuShares int    `json:"cpu_shares"`
}

// ManualCodeRequest - Manual testcase'lar bilan kodni bajarish uchun
type ManualCodeRequest struct {
	TestCases []app.TestCase `json:"test_cases"` // Manual testcase'lar
	Language  string         `json:"language"`
	Code      string         `json:"code"`
	TimeoutMs int            `json:"timeout_ms"`
	MemoryMb  int            `json:"memory_mb"`
	CpuShares int            `json:"cpu_shares"`
}

func handler(ctx *fasthttp.RequestCtx) {
	// CORS sozlamalari
	ctx.Response.Header.Set("Access-Control-Allow-Origin", ALLOWED_ORIGINS)
	ctx.Response.Header.Set("Access-Control-Allow-Methods", ALLOWED_METHODS)
	ctx.Response.Header.Set("Access-Control-Allow-Headers", ALLOWED_HEADERS)

	if string(ctx.Method()) == "OPTIONS" {
		ctx.SetStatusCode(fasthttp.StatusNoContent)
		return
	}

	switch string(ctx.Path()) {
	case "/ws":
		if string(ctx.Method()) == "GET" {
			err := upgrader.Upgrade(ctx, func(conn *websocket.Conn) {
				defer conn.Close()
				for {
					_, msg, err := conn.ReadMessage()
					if err != nil {
						log.Println("WebSocket read error:", err)
						break
					}
					log.Printf("Received via WebSocket: %s", msg)

					var req CodeRequest
					if err := json.Unmarshal(msg, &req); err != nil {
						log.Println("WebSocket JSON parse error:", err)
						conn.WriteMessage(websocket.TextMessage, []byte(`{"status": "Error", "error": "Invalid JSON format"}`))
						continue
					}

					// Default qiymatlar
					if req.TimeoutMs == 0 {
						req.TimeoutMs = 5000
					}
					if req.MemoryMb == 0 {
						req.MemoryMb = 128
					}
					if req.CpuShares == 0 {
						req.CpuShares = 512
					}

					// Problem ID orqali kodni bajarish
					result := app.ExecuteCodeWithProblemID(app.ExecutionRequest{
						ProblemID: req.ProblemID,
						Code:      req.Code,
						Language:  req.Language,
						TimeoutMs: req.TimeoutMs,
						MemoryMb:  req.MemoryMb,
						CpuShares: req.CpuShares,
					})

					responseBytes, err := json.Marshal(result)
					if err != nil {
						log.Println("WebSocket result marshal error:", err)
						responseBytes = []byte(`{"status": "Error", "error": "Failed to marshal result"}`)
					}
					if err = conn.WriteMessage(websocket.TextMessage, responseBytes); err != nil {
						log.Println("WebSocket write error:", err)
						break
					}
				}
			})
			if err != nil {
				log.Println("WebSocket upgrade error:", err)
				ctx.Error("WebSocket error", fasthttp.StatusInternalServerError)
			}
		} else {
			ctx.Error("Method not allowed for /ws", fasthttp.StatusMethodNotAllowed)
		}

	case "/execute":
		if string(ctx.Method()) == "POST" {
			body := ctx.PostBody()
			log.Printf("POST /execute request received. Body size: %d bytes", len(body))

			var req CodeRequest
			if err := json.Unmarshal(body, &req); err != nil {
				log.Printf("POST /execute JSON parse error: %v, Body: %s", err, body)
				ctx.Error(fmt.Sprintf("So'rovni parse qilishda xato: %v", err), fasthttp.StatusBadRequest)
				return
			}

			// Default qiymatlar
			if req.TimeoutMs == 0 {
				req.TimeoutMs = 5000
			}
			if req.MemoryMb == 0 {
				req.MemoryMb = 128
			}
			if req.CpuShares == 0 {
				req.CpuShares = 512
			}

			// Problem ID orqali kodni bajarish
			result := app.ExecuteCodeWithProblemID(app.ExecutionRequest{
				ProblemID: req.ProblemID,
				Code:      req.Code,
				Language:  req.Language,
				TimeoutMs: req.TimeoutMs,
				MemoryMb:  req.MemoryMb,
				CpuShares: req.CpuShares,
			})

			ctx.SetContentType("application/json")
			ctx.SetStatusCode(fasthttp.StatusOK)
			if err := json.NewEncoder(ctx).Encode(result); err != nil {
				log.Printf("Natijani JSON ga kodlashda xato: %v", err)
				ctx.Error("Natijani qaytarishda xato.", fasthttp.StatusInternalServerError)
			}
		} else {
			ctx.Error("Method not allowed for /execute", fasthttp.StatusMethodNotAllowed)
		}

	case "/execute-manual":
		if string(ctx.Method()) == "POST" {
			body := ctx.PostBody()
			log.Printf("POST /execute-manual request received. Body size: %d bytes", len(body))

			var req ManualCodeRequest
			if err := json.Unmarshal(body, &req); err != nil {
				log.Printf("POST /execute-manual JSON parse error: %v", err)
				ctx.Error(fmt.Sprintf("So'rovni parse qilishda xato: %v", err), fasthttp.StatusBadRequest)
				return
			}

			// Default qiymatlar
			if req.TimeoutMs == 0 {
				req.TimeoutMs = 5000
			}
			if req.MemoryMb == 0 {
				req.MemoryMb = 128
			}
			if req.CpuShares == 0 {
				req.CpuShares = 512
			}

			// Manual testcase'lar bilan kodni bajarish
			result := app.ExecuteCode(req.TestCases, app.ExecutionRequest{
				Code:      req.Code,
				Language:  req.Language,
				TimeoutMs: req.TimeoutMs,
				MemoryMb:  req.MemoryMb,
				CpuShares: req.CpuShares,
			})

			ctx.SetContentType("application/json")
			ctx.SetStatusCode(fasthttp.StatusOK)
			if err := json.NewEncoder(ctx).Encode(result); err != nil {
				log.Printf("Natijani JSON ga kodlashda xato: %v", err)
				ctx.Error("Natijani qaytarishda xato.", fasthttp.StatusInternalServerError)
			}
		} else {
			ctx.Error("Method not allowed for /execute-manual", fasthttp.StatusMethodNotAllowed)
		}

	default:
		ctx.Error("Not found", fasthttp.StatusNotFound)
	}
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env fayli topilmadi:", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Server running on :%s", port)
	err := fasthttp.ListenAndServe(":"+port, handler)

	if err != nil {
		log.Fatal("Server error:", err)
	}
}
