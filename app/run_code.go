package app // app katalogidagi paket

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ExecutionRequest - foydalanuvchidan keladigan so'rovning JSON strukturasini belgilaydi
// Bu struct main.go dagi Code structiga mos keladi
type ExecutionRequest struct {
	Code      string `json:"code"`
	Language  string `json:"language"`
	Stdin     string `json:"stdin"`
	TimeoutMs int    `json:"timeout_ms"` // Vaqt cheklovi millisekundlarda
	MemoryMb  int    `json:"memory_mb"`  // Xotira cheklovi megabaytlarda
	CpuShares int    `json:"cpu_shares"` // CPU ulushi (0-1024, 1024 = to'liq CPU)
}

// ExecutionResult - kod ijrosining natijasini qaytarish uchun JSON strukturasini belgilaydi
type ExecutionResult struct {
	Output   string `json:"output"`
	Error    string `json:"error"`
	Status   string `json:"status"` // "Accepted", "Time Limit Exceeded", "Runtime Error", "Memory Limit Exceeded", "Compilation Error", "Internal Error"
	TimeMs   int64  `json:"time_ms"`
	MemoryKb int64  `json:"memory_kb"` // Bu qismni aniq o'lchash murakkab, hozircha taxminiy yoki limitni ko'rsatamiz
}

// ExecuteCode - kodni Docker konteynerida bajarish uchun asosiy funksiya
func ExecuteCode(req ExecutionRequest) ExecutionResult {
	// Vaqtinchalik katalog yaratish. Har bir ijro uchun noyob katalog bo'ladi.
	// Bu, bir vaqtning o'zida bir nechta ijrolar bir-biriga xalaqit bermasligini ta'minlaydi.
	// `os.TempDir()` tizimning standart vaqtinchalik katalogini ishlatadi (odatda /tmp)
	tempDir, err := ioutil.TempDir(os.TempDir(), "code-execution-*")
	if err != nil {
		log.Printf("Vaqtinchalik katalog yaratishda xato: %v", err)
		return ExecutionResult{Status: "Internal Error", Error: "Serverda vaqtinchalik katalog yaratishda xato."}
	}
	// Funksiya tugagach, vaqtinchalik katalogni o'chirishni ta'minlash
	defer os.RemoveAll(tempDir)

	// Kod faylining nomi va yo'lini aniqlash
	codeFileName := getCodeFileName(req.Language)
	codeFilePath := filepath.Join(tempDir, codeFileName)

	// Kirish ma'lumotlari (stdin) faylining yo'lini aniqlash
	inputFilePath := filepath.Join(tempDir, "input.txt")

	// Kodni faylga yozish
	if err := ioutil.WriteFile(codeFilePath, []byte(req.Code), 0644); err != nil {
		log.Printf("Kod faylini yozishda xato: %v", err)
		return ExecutionResult{Status: "Internal Error", Error: "Kod faylini yozishda xato."}
	}

	// Agar kirish ma'lumotlari bo'lsa, ularni faylga yozish
	if req.Stdin != "" {
		if err := ioutil.WriteFile(inputFilePath, []byte(req.Stdin), 0644); err != nil {
			log.Printf("Input faylini yozishda xato: %v", err)
			return ExecutionResult{Status: "Internal Error", Error: "Input faylini yozishda xato."}
		}
	}

	// Docker tasviri va ijro buyrug'ini tilga qarab aniqlash
	dockerImage := getDockerImage(req.Language)
	runCommand := getRunCommand(req.Language, codeFileName, inputFilePath)

	// Docker buyrug'ini qurish
	// --rm: Konteyner ish tugagach avtomatik o'chiriladi
	// --network=none: Konteynerning tarmoqqa chiqishini butunlay o'chirish (xavfsizlik uchun muhim)
	// --memory: Xotira limiti (req.MemoryMb dan foydalanamiz)
	// --memory-swap: Swap xotira limiti (xotira limitiga teng bo'lishi tavsiya etiladi)
	// --cpu-shares: CPU ulushi (req.CpuShares dan foydalanamiz)
	// -v: Hostdagi vaqtinchalik katalogni konteyner ichidagi /app katalogiga mount qilish
	// --pids-limit: Konteynerda ishga tushirish mumkin bo'lgan jarayonlar sonini cheklash
	// --security-opt=no-new-privileges: Konteyner ichida imtiyozlarni ko'tarishni oldini olish
	// --cap-drop=ALL: Barcha Linux imtiyozlarini o'chirish
	cmdArgs := []string{
		"run", "--rm",
		"--network=none",
		fmt.Sprintf("--memory=%dm", req.MemoryMb),
		fmt.Sprintf("--memory-swap=%dm", req.MemoryMb),
		fmt.Sprintf("--cpu-shares=%d", req.CpuShares),
		"-v", fmt.Sprintf("%s:/app", tempDir), // Kodni mount qilish
		"--pids-limit=100", // Maksimal 100 ta jarayon
		"--security-opt=no-new-privileges",
		"--cap-drop=ALL",
		dockerImage,
	}
	cmdArgs = append(cmdArgs, runCommand...)

	// Vaqt cheklovini o'rnatish
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.TimeoutMs)*time.Millisecond)
	defer cancel() // Kontekstni bekor qilishni ta'minlash

	// Docker buyrug'ini bajarish
	cmd := exec.CommandContext(ctx, "docker", cmdArgs...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout // Standart chiqishni ushlash
	cmd.Stderr = &stderr // Xatolarni ushlash

	startTime := time.Now() // Ijro boshlanish vaqti
	err = cmd.Run()         // Buyruqni bajarish
	endTime := time.Now()   // Ijro tugash vaqti

	result := ExecutionResult{
		Output: stdout.String(),
		Error:  stderr.String(),
		TimeMs: endTime.Sub(startTime).Milliseconds(),
		// Xotira sarfini aniq o'lchash murakkab, bu yerda faqat limitni ko'rsatamiz
		MemoryKb: int64(req.MemoryMb * 1024),
	}

	// Natijalarni tahlil qilish va statusni aniqlash
	if ctx.Err() == context.DeadlineExceeded {
		result.Status = "Time Limit Exceeded"
	} else if err != nil {
		// Agar Docker buyrug'i xato bilan tugasa
		if exitError, ok := err.(*exec.ExitError); ok {
			// Kompilyatsiya xatosi yoki Runtime xatosi
			if req.Language == "java" || req.Language == "cpp" || req.Language == "go" { // Kompilyatsiya talab qiladigan tillar
				// Agar stderrda kompilyatsiya xatosi bo'lsa
				if strings.Contains(stderr.String(), "error:") ||
					strings.Contains(stderr.String(), "compilation failed") ||
					strings.Contains(stderr.String(), "undefined reference") { // C++ uchun
					result.Status = "Compilation Error"
				} else {
					result.Status = "Runtime Error"
				}
			} else {
				result.Status = "Runtime Error"
			}
			// Docker loglarida "OOMKilled" ni tekshirish orqali Memory Limit Exceeded ni aniqlash mumkin
			// Bu qismni qo'shimcha logika bilan kengaytirish mumkin
			if strings.Contains(stderr.String(), "OOMKilled") || strings.Contains(stdout.String(), "OOMKilled") {
				result.Status = "Memory Limit Exceeded"
			}
		} else {
			// Boshqa turdagi sistemaviy xatolar
			log.Printf("Docker buyrug'ini bajarishda kutilmagan xato: %v, stderr: %s", err, stderr.String())
			result.Status = "Internal Error"
			result.Error = fmt.Sprintf("Docker buyrug'ini bajarishda kutilmagan xato: %v", err)
		}
	} else {
		result.Status = "Accepted"
	}

	return result
}

// getCodeFileName - tilga qarab kod faylining nomini qaytaradi
func getCodeFileName(lang string) string {
	switch lang {
	case "python":
		return "main.py"
	case "java":
		return "Main.java" // Java'da class nomi Main bo'lishi kerak
	case "cpp":
		return "main.cpp"
	case "go":
		return "main.go"
	case "javascript":
		return "index.js"
	default:
		return "main.txt" // Noma'lum til uchun default
	}
}

// getDockerImage - tilga qarab Docker tasvirining nomini qaytaradi
func getDockerImage(lang string) string {
	switch lang {
	case "python":
		return "python:3.12.10-alpine"
	case "java":
		return "openjdk:17-jdk-slim" // Yoki openjdk:21-jdk-slim
	case "cpp":
		return "gcc:latest" // GCC kompilyatori bilan
	case "go":
		return "golang:1.22-alpine"
	case "javascript":
		return "node:22.16.0-alpine"
	default:
		return "alpine/git" // Placeholder
	}
}

// getRunCommand - tilga qarab kodni bajarish uchun Docker ichidagi buyruqni qaytaradi
func getRunCommand(lang, codeFileName, inputFilePath string) []string {
	// Input fayli mavjud bo'lsa, uni kodga yo'naltirish uchun
	inputRedirect := ""
	// filepath.Base(inputFilePath) faqat fayl nomini oladi (masalan, "input.txt")
	// Bu Docker konteyneri ichidagi /app/input.txt ga mos keladi
	if _, err := os.Stat(inputFilePath); err == nil { // Fayl mavjudligini tekshirish
		inputRedirect = fmt.Sprintf("< /app/%s", filepath.Base(inputFilePath))
	}

	switch lang {
	case "python":
		return []string{"sh", "-c", fmt.Sprintf("python /app/%s %s", codeFileName, inputRedirect)}
	case "java":
		// Java uchun kompilyatsiya va keyin ijro
		return []string{"sh", "-c", fmt.Sprintf("javac /app/%s && java -classpath /app Main %s", codeFileName, inputRedirect)}
	case "cpp":
		// C++ uchun kompilyatsiya va keyin ijro
		return []string{"sh", "-c", fmt.Sprintf("g++ -o /app/a.out /app/%s && /app/a.out %s", codeFileName, inputRedirect)}
	case "go":
		// Go uchun kompilyatsiya va keyin ijro
		return []string{"sh", "-c", fmt.Sprintf("go run /app/%s %s", codeFileName, inputRedirect)}
	case "javascript":
		// JavaScript (Node.js) uchun ijro
		return []string{"sh", "-c", fmt.Sprintf("node /app/%s %s", codeFileName, inputRedirect)}
	default:
		return []string{"echo", "Qo'llab-quvvatlanmaydigan dasturlash tili."}
	}
}
