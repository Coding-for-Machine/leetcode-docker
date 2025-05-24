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
		return true // Barcha originlarni qabul qilish (xavfsizlik uchun ishlab chiqarishda cheklash tavsiya etiladi)
	},
}

// Code structi endi app.ExecutionRequest bilan bir xil maydonlarga ega
type Code struct {
	Language  string `json:"language"`
	Code      string `json:"code"`
	Input     string `json:"input"`
	TimeoutMs int    `json:"timeout_ms"` // Vaqt cheklovi millisekundlarda
	MemoryMb  int    `json:"memory_mb"`  // Xotira cheklovi megabaytlarda
	CpuShares int    `json:"cpu_shares"` // CPU ulushi (0-1024, 1024 = to'liq CPU)
}

func handler(ctx *fasthttp.RequestCtx) {
	// CORS sozlamalari (handler ichiga ko'chirildi, chunki fasthttp.ListenAndServe ichidagi funksiya har bir so'rov uchun ishlaydi)
	ctx.Response.Header.Set("Access-Control-Allow-Origin", ALLOWED_ORIGINS)
	ctx.Response.Header.Set("Access-Control-Allow-Methods", ALLOWED_METHODS)
	ctx.Response.Header.Set("Access-Control-Allow-Headers", ALLOWED_HEADERS)

	// Preflight so'rovlar (OPTIONS)
	if string(ctx.Method()) == "OPTIONS" {
		ctx.SetStatusCode(fasthttp.StatusNoContent)
		return
	}

	switch string(ctx.Path()) {
	case "/ws": // WebSocket aloqasi uchun
		if string(ctx.Method()) == "GET" {
			err := upgrader.Upgrade(ctx, func(conn *websocket.Conn) {
				defer conn.Close()
				for {
					// Clientdan xabar o'qish
					_, msg, err := conn.ReadMessage() //msgType
					if err != nil {
						log.Println("WebSocket read error:", err)
						break
					}
					log.Printf("Received via WebSocket: %s", msg)

					// WebSocket orqali kelgan kodni bajarish (JSON formatida kelishi kerak)
					var req Code
					if err := json.Unmarshal(msg, &req); err != nil {
						log.Println("WebSocket JSON parse error:", err)
						conn.WriteMessage(websocket.TextMessage, []byte(`{"status": "Error", "error": "Invalid JSON format"}`))
						continue
					}

					// app.ExecuteCode funksiyasini chaqirish
					result := app.ExecuteCode(app.ExecutionRequest{
						Code:      req.Code,
						Language:  req.Language,
						TimeoutMs: req.TimeoutMs,
						MemoryMb:  req.MemoryMb,
						CpuShares: req.CpuShares,
					})

					// Natijani JSON formatida WebSocket orqali qaytarish
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

	case "/execute": // HTTP POST orqali kodni bajarish uchun
		if string(ctx.Method()) == "POST" {
			body := ctx.PostBody()
			log.Printf("POST /execute request received. Body size: %d bytes", len(body))

			var req Code
			// JSON request body'ni Code structiga to'g'ri tahlil qilish
			if err := json.Unmarshal(body, &req); err != nil {
				log.Printf("POST /execute JSON parse error: %v, Body: %s", err, body)
				ctx.Error(fmt.Sprintf("So'rovni parse qilishda xato: %v", err), fasthttp.StatusBadRequest)
				return
			}

			// Default qiymatlarni o'rnatish, agar ular berilmagan bo'lsa
			if req.TimeoutMs == 0 {
				req.TimeoutMs = 5000 // 5 soniya
			}
			if req.MemoryMb == 0 {
				req.MemoryMb = 128 // 128 MB
			}
			if req.CpuShares == 0 {
				req.CpuShares = 512 // Yarim CPU ulushi
			}

			// app.ExecuteCode funksiyasini chaqirish
			result := app.ExecuteCode(app.ExecutionRequest{
				Code:      req.Code,
				Language:  req.Language,
				TimeoutMs: req.TimeoutMs,
				MemoryMb:  req.MemoryMb,
				CpuShares: req.CpuShares,
			})

			// Natijani JSON formatida qaytarish
			ctx.SetContentType("application/json")
			ctx.SetStatusCode(fasthttp.StatusOK)
			if err := json.NewEncoder(ctx).Encode(result); err != nil {
				log.Printf("Natijani JSON ga kodlashda xato: %v", err)
				ctx.Error("Natijani qaytarishda xato.", fasthttp.StatusInternalServerError)
			}
		} else {
			ctx.Error("Method not allowed for /execute", fasthttp.StatusMethodNotAllowed)
		}

	default:
		ctx.Error("Not found", fasthttp.StatusNotFound)
	}
}

func main() {
	// .env faylni yuklash
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env fayli topilmadi yoki yuklashda xato:", err)
		// .env fayl majburiy emas, shuning uchun Fatal o'rniga Println ishlatamiz
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // Default portni 8080 ga o'zgartirdik, chunki docker-compose da shunday
	}

	// Serverni ishga tushirish
	log.Printf("Server running on :%s", port)
	err := fasthttp.ListenAndServe(":"+port, handler) // CORS logikasi handler ichiga ko'chirildi

	if err != nil {
		log.Fatal("Server error:", err)
	}
}
