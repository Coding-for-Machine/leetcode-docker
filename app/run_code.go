package app

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
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

// TestCase - bitta test case uchun kirish va kutilgan natija
type TestCase struct {
	ID         int    `json:"id"`
	InputText  string `json:"input_text"`
	OutputText string `json:"output_text"`
}

// IndividualTestResult - bitta test case ijrosining natijasi
type IndividualTestResult struct {
	ID         int     `json:"id"`
	InputText  string  `json:"input_text"`
	OutputText string  `json:"output_text"`
	Actual     string  `json:"actual"`
	IsCorrect  bool    `json:"is_correct"`
	TimeMs     int64   `json:"time_ms"`
	MemoryKb   float64 `json:"memory_kb"`
	Error      string  `json:"error,omitempty"`
	Status     string  `json:"status,omitempty"`
}

// ExecutionRequest - foydalanuvchidan keladigan so'rov
type ExecutionRequest struct {
	ProblemID int    `json:"problem_id"` // Yangi: problem ID orqali testcase'larni olish
	Code      string `json:"code"`
	Language  string `json:"language"`
	TimeoutMs int    `json:"timeout_ms"`
	MemoryMb  int    `json:"memory_mb"`
	CpuShares int    `json:"cpu_shares"`
}

// ExecutionResult - kod ijrosining umumiy natijasi
type ExecutionResult struct {
	OverallStatus string                 `json:"overall_status"`
	TestResults   []IndividualTestResult `json:"test_results"`
	TotalTests    int                    `json:"total_tests"`
	PassedTests   int                    `json:"passed_tests"`
}

// NeonDB - Ma'lumotlar bazasidan testcase'larni olish
func NeonDB(problemID int) ([]TestCase, error) {
	// .env faylini yuklash
	err := godotenv.Load()
	if err != nil {
		log.Printf("Warning: .env fayli yuklanmadi: %v", err)
	}

	// Ma'lumotlar bazasiga ulanish
	connStr := os.Getenv("DATABASE_URL")
	if connStr == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable topilmadi")
	}

	conn, err := pgx.Connect(context.Background(), connStr)
	if err != nil {
		return nil, fmt.Errorf("ma'lumotlar bazasiga ulanishda xato: %v", err)
	}
	defer conn.Close(context.Background())

	// Testcase'larni olish
	query := "SELECT id, input_text, output_text FROM problems_testcases WHERE problem_id = $1 ORDER BY id"
	rows, err := conn.Query(context.Background(), query, problemID)
	if err != nil {
		return nil, fmt.Errorf("so'rovni bajarishda xato: %v", err)
	}
	defer rows.Close()

	var testcases []TestCase
	for rows.Next() {
		var tc TestCase
		err := rows.Scan(&tc.ID, &tc.InputText, &tc.OutputText)
		if err != nil {
			return nil, fmt.Errorf("ma'lumotni o'qishda xato: %v", err)
		}
		testcases = append(testcases, tc)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteratsiyasida xato: %v", err)
	}

	if len(testcases) == 0 {
		return nil, fmt.Errorf("problem ID %d uchun testcase'lar topilmadi", problemID)
	}

	return testcases, nil
}

// ExecuteCodeWithProblemID - Problem ID orqali kodni bajarish
func ExecuteCodeWithProblemID(req ExecutionRequest) ExecutionResult {
	// Ma'lumotlar bazasidan testcase'larni olish
	testCases, err := NeonDB(req.ProblemID)
	if err != nil {
		log.Printf("Testcase'larni olishda xato: %v", err)
		return ExecutionResult{
			OverallStatus: "Database Error",
			TestResults: []IndividualTestResult{{
				Status: "Error",
				Error:  fmt.Sprintf("Testcase'larni olishda xato: %v", err),
			}},
		}
	}

	log.Printf("Problem ID %d uchun %d ta testcase topildi", req.ProblemID, len(testCases))

	// Eski ExecuteCode funksiyasini ishlatish
	legacyReq := ExecutionRequest{
		Code:      req.Code,
		Language:  req.Language,
		TimeoutMs: req.TimeoutMs,
		MemoryMb:  req.MemoryMb,
		CpuShares: req.CpuShares,
	}

	return executeCodeInternal(testCases, legacyReq)
}

// Eski ExecuteCode funksiyasi (testcase'lar to'g'ridan-to'g'ri berilganda)
func ExecuteCode(testCases []TestCase, req ExecutionRequest) ExecutionResult {
	return executeCodeInternal(testCases, req)
}

// executeCodeInternal - asosiy bajarish logikasi
func executeCodeInternal(testCases []TestCase, req ExecutionRequest) ExecutionResult {
	overallResult := ExecutionResult{
		OverallStatus: "Processing",
		TestResults:   []IndividualTestResult{},
		TotalTests:    len(testCases),
		PassedTests:   0,
	}

	if len(testCases) == 0 {
		overallResult.OverallStatus = "No Test Cases"
		return overallResult
	}

	// Parallel bajarish
	var wg sync.WaitGroup
	resultsChan := make(chan IndividualTestResult, len(testCases))

	for i, tc := range testCases {
		wg.Add(1)
		go func(testCase TestCase, index int) {
			defer wg.Done()

			testName := fmt.Sprintf("test-%d (ID: %d)", index+1, testCase.ID)
			log.Printf("%s tilida Test Case ID: %d ni bajarish: testcase: %v", req.Language, testCase.ID, testName)

			tempDir, err := ioutil.TempDir(os.TempDir(), fmt.Sprintf("code-execution-%d-*", testCase.ID))
			if err != nil {
				resultsChan <- IndividualTestResult{
					ID: testCase.ID, InputText: testCase.InputText, OutputText: testCase.OutputText,
					Status: "Internal Error", Error: fmt.Sprintf("Vaqtinchalik katalog yaratishda xato: %v", err),
				}
				return
			}
			defer os.RemoveAll(tempDir)

			codeFileName := getCodeFileName(req.Language)
			codeFilePath := filepath.Join(tempDir, codeFileName)
			inputFilePath := filepath.Join(tempDir, "input.txt")

			if err := ioutil.WriteFile(codeFilePath, []byte(req.Code), 0644); err != nil {
				resultsChan <- IndividualTestResult{
					ID: testCase.ID, InputText: testCase.InputText, OutputText: testCase.OutputText,
					Status: "Internal Error", Error: fmt.Sprintf("Kod faylini yozishda xato: %v", err),
				}
				return
			}

			if err := ioutil.WriteFile(inputFilePath, []byte(testCase.InputText), 0644); err != nil {
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
				"-v", fmt.Sprintf("%s:/app", tempDir),
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

			actualOutput := strings.TrimSpace(stdout.String())
			expectedOutput := strings.TrimSpace(testCase.OutputText)

			testResult := IndividualTestResult{
				ID:         testCase.ID,
				InputText:  testCase.InputText,
				OutputText: testCase.OutputText,
				Actual:     actualOutput,
				IsCorrect:  false,
				TimeMs:     endTime.Sub(startTime).Milliseconds(),
				MemoryKb:   float64(req.MemoryMb * 1024),
				Error:      stderr.String(),
			}

			// Status aniqlash
			if ctx.Err() == context.DeadlineExceeded {
				testResult.Status = "Time Limit Exceeded"
			} else if cmdErr != nil {
				if _, ok := cmdErr.(*exec.ExitError); ok {
					if req.Language == "java" || req.Language == "cpp" || req.Language == "go" {
						if strings.Contains(stderr.String(), "error:") ||
							strings.Contains(stderr.String(), "compilation failed") {
							testResult.Status = "Compilation Error"
						} else {
							testResult.Status = "Runtime Error"
						}
					} else {
						testResult.Status = "Runtime Error"
					}
					if strings.Contains(stderr.String(), "OOMKilled") {
						testResult.Status = "Memory Limit Exceeded"
					}
				} else {
					testResult.Status = "Internal Error"
					testResult.Error = fmt.Sprintf("Docker xatosi: %v", cmdErr)
				}
			} else {
				if actualOutput == expectedOutput {
					testResult.Status = "Accepted"
					testResult.IsCorrect = true
				} else {
					testResult.Status = "Wrong Answer"
				}
			}

			resultsChan <- testResult
		}(tc, i)
	}

	wg.Wait()
	close(resultsChan)

	// Natijalarni yig'ish
	allTestsPassed := true
	for res := range resultsChan {
		overallResult.TestResults = append(overallResult.TestResults, res)
		if res.IsCorrect {
			overallResult.PassedTests++
		} else {
			allTestsPassed = false
		}
	}

	// Umumiy status
	if allTestsPassed {
		overallResult.OverallStatus = "Accepted"
	} else {
		// Birinchi xatoga qarab status
		for _, res := range overallResult.TestResults {
			if !res.IsCorrect {
				overallResult.OverallStatus = res.Status
				break
			}
		}
	}

	return overallResult
}

// Qolgan helper funksiyalar bir xil...
func getCodeFileName(lang string) string {
	switch lang {
	case "python":
		return "main.py"
	case "java":
		return "Main.java"
	case "cpp":
		return "main.cpp"
	case "go":
		return "main.go"
	case "javascript":
		return "index.js"
	default:
		return "main.txt"
	}
}

func getDockerImage(lang string) string {
	switch lang {
	case "python":
		return "python:3.12.10-alpine"
	case "java":
		return "openjdk:17-jdk-slim"
	case "cpp":
		return "gcc:latest"
	case "go":
		return "golang:1.22-alpine"
	case "javascript":
		return "node:22.16.0-alpine"
	default:
		return "alpine/git"
	}
}

func getRunCommand(lang, codeFileName, inputFilePath string) []string {
	inputRedirect := ""
	if _, err := os.Stat(inputFilePath); err == nil {
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
		return []string{"echo", "Qo'llab-quvvatlanmaydigan til."}
	}
}
