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
	"sync" // Goroutines bilan ishlash uchun
	"time"
)

// TestCase - bitta test case uchun kirish va kutilgan natija
// Endi id maydonini o'z ichiga oladi
type TestCase struct {
	ID         int    `json:"id"`          // Test case IDsi
	InputText  string `json:"input_text"`  // Kirish ma'lumotlari
	OutputText string `json:"output_text"` // Kutilgan natija
	// is_correct maydoni bu yerda kerak emas, chunki bu judge tomonidan aniqlanadi
}

// IndividualTestResult - bitta test case ijrosining natijasi
type IndividualTestResult struct {
	ID         int     `json:"id"`          // Test case IDsi
	InputText  string  `json:"input_text"`  // Kirish ma'lumotlari
	OutputText string  `json:"output_text"` // Kutilgan natija
	Actual     string  `json:"actual"`      // Koddan chiqqan haqiqiy natija
	IsCorrect  bool    `json:"is_correct"`  // Test case to'g'ri o'tdimi
	TimeMs     int64   `json:"time_ms"`
	MemoryKb   float64 `json:"memory_kb"`
	Error      string  `json:"error,omitempty"`  // Agar xato bo'lsa
	Status     string  `json:"status,omitempty"` // TLE, RTE, MLE, CE kabi statuslar
}

// ExecutionRequest - foydalanuvchidan keladigan so'rovning JSON strukturasini belgilaydi
// Endi test_cases ro'yxatini to'g'ridan-to'g'ri qabul qiladi
type ExecutionRequest struct {
	TestCases []TestCase `json:"test_cases"` // Yangi: test case'lar ro'yxati
	Code      string     `json:"code"`
	Language  string     `json:"language"`
	TimeoutMs int        `json:"timeout_ms"` // Vaqt cheklovi millisekundlarda
	MemoryMb  int        `json:"memory_mb"`  // Xotira cheklovi megabaytlarda
	CpuShares int        `json:"cpu_shares"` // CPU ulushi (0-1024, 1024 = to'liq CPU)
}

// ExecutionResult - kod ijrosining umumiy natijasi (barcha test case'lar bo'yicha)
type ExecutionResult struct {
	OverallStatus string                 `json:"overall_status"` // "Accepted", "Wrong Answer", "Time Limit Exceeded"
	TestResults   []IndividualTestResult `json:"test_results"`
}

// problemStore va getProblemTestCases endi kerak emas, chunki test case'lar so'rovdan keladi.
// var problemStore = map[string][]TestCase{...}
// func getProblemTestCases(problemID string) ([]TestCase, error) {...}

// ExecuteCode - kodni Docker konteynerida barcha test case'lar bo'yicha bajarish uchun asosiy funksiya
func ExecuteCode(req ExecutionRequest) ExecutionResult {
	overallResult := ExecutionResult{
		OverallStatus: "Processing", // Boshlang'ich status
		TestResults:   []IndividualTestResult{},
	}

	if len(req.TestCases) == 0 {
		overallResult.OverallStatus = "No Test Cases Provided"
		overallResult.TestResults = append(overallResult.TestResults, IndividualTestResult{
			Status: "Error", Error: "Kodni bajarish uchun test case'lar berilmagan.",
		})
		return overallResult
	}

	// Test case'larni parallel bajarish uchun wait group
	var wg sync.WaitGroup
	resultsChan := make(chan IndividualTestResult, len(req.TestCases))

	for i, tc := range req.TestCases {
		wg.Add(1)
		go func(testCase TestCase) {
			defer wg.Done()

			testName := fmt.Sprintf("test-%d (ID: %d)", i+1, testCase.ID)
			log.Printf("Executing %s for Test Case ID: %d", req.Language, testCase.ID)

			tempDir, err := ioutil.TempDir(os.TempDir(), fmt.Sprintf("code-execution-%d-*", testCase.ID))
			if err != nil {
				log.Printf("Vaqtinchalik katalog yaratishda xato (%s): %v", testName, err)
				resultsChan <- IndividualTestResult{
					ID: testCase.ID, InputText: testCase.InputText, OutputText: testCase.OutputText,
					Status: "Internal Error", Error: fmt.Sprintf("Serverda vaqtinchalik katalog yaratishda xato: %v", err),
				}
				return
			}
			defer os.RemoveAll(tempDir) // Funksiya tugagach, vaqtinchalik katalogni o'chirish

			codeFileName := getCodeFileName(req.Language)
			codeFilePath := filepath.Join(tempDir, codeFileName)
			inputFilePath := filepath.Join(tempDir, "input.txt")

			if err := ioutil.WriteFile(codeFilePath, []byte(req.Code), 0644); err != nil {
				log.Printf("Kod faylini yozishda xato (%s): %v", testName, err)
				resultsChan <- IndividualTestResult{
					ID: testCase.ID, InputText: testCase.InputText, OutputText: testCase.OutputText,
					Status: "Internal Error", Error: fmt.Sprintf("Kod faylini yozishda xato: %v", err),
				}
				return
			}
			if err := ioutil.WriteFile(inputFilePath, []byte(testCase.InputText), 0644); err != nil {
				log.Printf("Input faylini yozishda xato (%s): %v", testName, err)
				resultsChan <- IndividualTestResult{
					ID: testCase.ID, InputText: testCase.InputText, OutputText: testCase.OutputText,
					Status: "Internal Error", Error: fmt.Sprintf("Input faylini yozishda xato: %v", err),
				}
				return
			}

			dockerImage := getDockerImage(req.Language)
			runCommand := getRunCommand(req.Language, codeFileName, inputFilePath)

			cmdArgs := []string{
				"run", "--rm",
				"--network=none",
				fmt.Sprintf("--memory=%dm", req.MemoryMb),
				fmt.Sprintf("--memory-swap=%dm", req.MemoryMb),
				fmt.Sprintf("--cpu-shares=%d", req.CpuShares),
				"-v", fmt.Sprintf("%s:/app", tempDir), // Kodni mount qilish
				"--pids-limit=100",
				"--security-opt=no-new-privileges",
				"--cap-drop=ALL",
				dockerImage,
			}
			cmdArgs = append(cmdArgs, runCommand...)

			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.TimeoutMs)*time.Millisecond)
			defer cancel()

			cmd := exec.CommandContext(ctx, "docker", cmdArgs...)

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			startTime := time.Now()
			cmdErr := cmd.Run()
			endTime := time.Now()

			actualOutput := strings.TrimSpace(stdout.String())       // Bo'shliqlarni olib tashlash
			expectedOutput := strings.TrimSpace(testCase.OutputText) // Bo'shliqlarni olib tashlash

			testResult := IndividualTestResult{
				ID:         testCase.ID,
				InputText:  testCase.InputText,
				OutputText: testCase.OutputText,
				Actual:     actualOutput,
				IsCorrect:  false, // Default false
				TimeMs:     endTime.Sub(startTime).Milliseconds(),
				MemoryKb:   float64(req.MemoryMb * 1024), // Hozircha faqat limitni ko'rsatamiz
				Error:      stderr.String(),
			}

			if ctx.Err() == context.DeadlineExceeded {
				testResult.Status = "Time Limit Exceeded"
				testResult.IsCorrect = false
			} else if cmdErr != nil {
				if _, ok := cmdErr.(*exec.ExitError); ok { //exitError
					if req.Language == "java" || req.Language == "cpp" || req.Language == "go" {
						if strings.Contains(stderr.String(), "error:") ||
							strings.Contains(stderr.String(), "compilation failed") ||
							strings.Contains(stderr.String(), "undefined reference") {
							testResult.Status = "Compilation Error"
						} else {
							testResult.Status = "Runtime Error"
						}
					} else {
						testResult.Status = "Runtime Error"
					}
					if strings.Contains(stderr.String(), "OOMKilled") || strings.Contains(stdout.String(), "OOMKilled") {
						testResult.Status = "Memory Limit Exceeded"
					}
				} else {
					log.Printf("Docker buyrug'ini bajarishda kutilmagan xato (%s): %v, stderr: %s", testName, cmdErr, stderr.String())
					testResult.Status = "Internal Error"
					testResult.Error = fmt.Sprintf("Docker buyrug'ini bajarishda kutilmagan xato: %v", cmdErr)
				}
				testResult.IsCorrect = false
			} else {
				if actualOutput == expectedOutput {
					testResult.Status = "Accepted"
					testResult.IsCorrect = true
				} else {
					testResult.Status = "Wrong Answer"
					testResult.IsCorrect = false
				}
			}
			resultsChan <- testResult
		}(tc) // testCase ni goroutine'ga uzatish
	}

	// Barcha goroutine'lar tugashini kutish
	wg.Wait()
	close(resultsChan) // Channelni yopish

	// Natijalarni yig'ish
	allTestsPassed := true
	for res := range resultsChan {
		overallResult.TestResults = append(overallResult.TestResults, res)
		if !res.IsCorrect { // IsCorrect bo'lmasa, umumiy status Accepted bo'lmaydi
			allTestsPassed = false
		}
	}

	// Umumiy statusni aniqlash
	if allTestsPassed {
		overallResult.OverallStatus = "Accepted"
	} else {
		// Agar bittasi ham o'tmasa, umumiy statusni aniqlash
		hasWrongAnswer := false
		hasTLE := false
		hasRTE := false
		hasMLE := false
		hasCE := false
		hasInternalError := false

		for _, res := range overallResult.TestResults {
			if res.Status == "Wrong Answer" {
				hasWrongAnswer = true
			} else if res.Status == "Time Limit Exceeded" {
				hasTLE = true
			} else if res.Status == "Runtime Error" {
				hasRTE = true
			} else if res.Status == "Memory Limit Exceeded" {
				hasMLE = true
			} else if res.Status == "Compilation Error" {
				hasCE = true
			} else if res.Status == "Internal Error" {
				hasInternalError = true
			}
		}

		if hasInternalError {
			overallResult.OverallStatus = "Internal Error"
		} else if hasCE {
			overallResult.OverallStatus = "Compilation Error"
		} else if hasTLE {
			overallResult.OverallStatus = "Time Limit Exceeded"
		} else if hasMLE {
			overallResult.OverallStatus = "Memory Limit Exceeded"
		} else if hasRTE {
			overallResult.OverallStatus = "Runtime Error"
		} else if hasWrongAnswer {
			overallResult.OverallStatus = "Wrong Answer"
		} else {
			overallResult.OverallStatus = "Failed" // Boshqa noma'lum xato
		}
	}

	return overallResult
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
		return []string{"sh", "-c", fmt.Sprintf("javac /app/%s && java -classpath /app Main %s", codeFileName, inputRedirect)}
	case "cpp":
		return []string{"sh", "-c", fmt.Sprintf("g++ -o /app/a.out /app/%s && /app/a.out %s", codeFileName, inputRedirect)}
	case "go":
		return []string{"sh", "-c", fmt.Sprintf("go run /app/%s %s", codeFileName, inputRedirect)}
	case "javascript":
		return []string{"sh", "-c", fmt.Sprintf("node /app/%s %s", codeFileName, inputRedirect)}
	default:
		return []string{"echo", "Qo'llab-quvvatlanmaydigan dasturlash tili."}
	}
}
