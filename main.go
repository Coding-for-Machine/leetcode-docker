package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/Coding-for-Machine/leetcode-docker/app" // app paketini import qilish
	"github.com/fasthttp/websocket"
	"github.com/joho/godotenv"
	"github.com/valyala/fasthttp"
)

const (
	ALLOWED_ORIGINS = "*"
	ALLOWED_METHODS = "GET, POST, OPTIONS" // OPTIONS faqat CORS preflight uchun
	ALLOWED_HEADERS = "Content-Type"
)

var upgrader = websocket.FastHTTPUpgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
		return true // Barcha originlarni qabul qilish (xavfsizlik uchun ishlab chiqarishda cheklash tavsiya etiladi)
	},
}

// RequestPayload - WebSocket orqali keladigan umumiy so'rov yuklamasi
// Bu struct app.ExecutionRequest bilan bir xil maydonlarga ega
type RequestPayload struct {
	ProblemID   int            `json:"problem_id,omitempty"`   // Problem ID asosida test
	CustomInput string         `json:"custom_input,omitempty"` // Custom input asosida test
	TestCases   []app.TestCase `json:"test_cases,omitempty"`   // Manual test case'lar asosida test
	Code        string         `json:"code"`
	Language    string         `json:"language"`
	TimeoutMs   int            `json:"timeout_ms"`
	MemoryMb    int            `json:"memory_mb"`
	CpuShares   int            `json:"cpu_shares"`
}

func handler(ctx *fasthttp.RequestCtx) {
	// CORS sozlamalari
	ctx.Response.Header.Set("Access-Control-Allow-Origin", ALLOWED_ORIGINS)
	ctx.Response.Header.Set("Access-Control-Allow-Methods", ALLOWED_METHODS)
	ctx.Response.Header.Set("Access-Control-Allow-Headers", ALLOWED_HEADERS)

	// Preflight so'rovlar (OPTIONS)
	if string(ctx.Method()) == "OPTIONS" {
		ctx.SetStatusCode(fasthttp.StatusNoContent)
		return
	}

	switch string(ctx.Path()) {
	case "/ws": // Faqat WebSocket aloqasi uchun
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

					var req RequestPayload
					if err := json.Unmarshal(msg, &req); err != nil {
						log.Println("WebSocket JSON parse error:", err)
						conn.WriteMessage(websocket.TextMessage, []byte(`{"overall_status": "Error", "error": "Invalid JSON format"}`))
						continue
					}

					// Default qiymatlarni o'rnatish
					if req.TimeoutMs == 0 {
						req.TimeoutMs = 5000
					}
					if req.MemoryMb == 0 {
						req.MemoryMb = 128
					}
					if req.CpuShares == 0 {
						req.CpuShares = 512
					}

					// app.ExecuteCode funksiyasini chaqirish (u endi barcha test turlarini boshqaradi)
					result := app.ExecuteCode(app.ExecutionRequest{
						ProblemID:   req.ProblemID,
						CustomInput: req.CustomInput,
						TestCases:   req.TestCases,
						Code:        req.Code,
						Language:    req.Language,
						TimeoutMs:   req.TimeoutMs,
						MemoryMb:    req.MemoryMb,
						CpuShares:   req.CpuShares,
					})

					// Natijani JSON formatida WebSocket orqali qaytarish
					responseBytes, err := json.Marshal(result)
					if err != nil {
						log.Println("WebSocket result marshal error:", err)
						responseBytes = []byte(`{"overall_status": "Error", "error": "Failed to marshal result"}`)
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

	default:
		ctx.Error("Not found", fasthttp.StatusNotFound)
	}
}

func main() {
	// .env faylini yuklash
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env fayli topilmadi yoki yuklashda xato:", err)
		// .env fayl majburiy emas, shuning uchun Fatal o'rniga Println ishlatamiz
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // Default portni 8080 ga o'zgartirdik, chunki docker-compose da shunday
	}

	log.Printf("Server running on :%s", port)
	err := fasthttp.ListenAndServe(":"+port, handler)

	if err != nil {
		log.Fatal("Server error:", err)
	}
}
