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
	ID         int     `json:"id,omitempty"` // Test case IDsi (Problem IDga asoslangan testlar uchun)
	InputText  string  `json:"input_text"`
	OutputText string  `json:"output_text,omitempty"` // Kutilgan natija (custom inputda bo'lmasligi mumkin)
	Actual     string  `json:"actual"`
	IsCorrect  bool    `json:"is_correct"` // Faqat OutputText mavjud bo'lganda tekshiriladi
	TimeMs     int64   `json:"time_ms"`
	MemoryKb   float64 `json:"memory_kb"`
	Error      string  `json:"error,omitempty"`
	Status     string  `json:"status,omitempty"` // TLE, RTE, MLE, CE kabi statuslar
}

// ExecutionRequest - WebSocket orqali keladigan umumiy so'rov strukturasini belgilaydi
// Bu struct barcha test turlarini (Problem ID, Custom Input, Manual Test Cases) qo'llab-quvvatlaydi
type ExecutionRequest struct {
	ProblemID   int        `json:"problem_id,omitempty"`   // Agar problem ID bo'lsa
	CustomInput string     `json:"custom_input,omitempty"` // Agar custom input bo'lsa
	TestCases   []TestCase `json:"test_cases,omitempty"`   // Agar test case'lar to'g'ridan-to'g'ri berilsa (manual)
	Code        string     `json:"code"`
	Language    string     `json:"language"`
	TimeoutMs   int        `json:"timeout_ms"`
	MemoryMb    int        `json:"memory_mb"`
	CpuShares   int        `json:"cpu_shares"`
}

// ExecutionResult - kod ijrosining umumiy natijasi
type ExecutionResult struct {
	ProblemID     int                    `json:"problem_id,omitempty"`
	OverallStatus string                 `json:"overall_status"`
	TestResults   []IndividualTestResult `json:"test_results"`
	TotalTests    int                    `json:"total_tests"`
	PassedTests   int                    `json:"passed_tests"`
	Error         string                 `json:"error,omitempty"` // Umumiy xato xabari
}

// NeonDB - Ma'lumotlar bazasidan testcase'larni olish
func NeonDB(problemID int) ([]TestCase, error) {
	if err := godotenv.Load(); err != nil {
		log.Printf("Warning: .env fayli yuklanmadi (NeonDB): %v", err)
	}

	connStr := os.Getenv("DATABASE_URL")
	if connStr == "" {
		return nil, fmt.Errorf("DATABASE_URL muhit o'zgaruvchisi topilmadi")
	}

	conn, err := pgx.Connect(context.Background(), "postgresql://leetcode_owner:npg_LtPQ6Arb9dJB@ep-polished-shadow-a24k41kj-pooler.eu-central-1.aws.neon.tech/leetcode?sslmode=require")
	if err != nil {
		return nil, fmt.Errorf("ma'lumotlar bazasiga ulanishda xato: %v", err)
	}
	defer conn.Close(context.Background())

	// Testcase'larni olish
	// E'tibor bering: sizning so'rovingizda `input_txt` va `output_txt` nomlari ishlatilgan.
	// Agar DB ustun nomlari `input_text` va `output_text` bo'lsa, so'rovni shunga moslang.
	query := "SELECT id, input_txt, output_txt FROM problems_testcase WHERE problem_id=$1 ORDER BY id"
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

// executeSingleTestCase - bitta test case uchun kodni bajarishning asosiy logikasi
func executeSingleTestCase(code, language, input string, timeoutMs, memoryMb, cpuShares int, testID int, expectedOutput string) IndividualTestResult {
	testResult := IndividualTestResult{
		ID:         testID,
		InputText:  input,
		OutputText: expectedOutput,
		IsCorrect:  false,                    // Default false
		MemoryKb:   float64(memoryMb * 1024), // Hozircha faqat limitni ko'rsatamiz
	}

	tempDir, err := ioutil.TempDir(os.TempDir(), fmt.Sprintf("code-execution-%d-*", testID))
	if err != nil {
		log.Printf("Vaqtinchalik katalog yaratishda xato (Test ID: %d): %v", testID, err)
		testResult.Status = "Internal Error"
		testResult.Error = fmt.Sprintf("Serverda vaqtinchalik katalog yaratishda xato: %v", err)
		return testResult
	}
	defer os.RemoveAll(tempDir)

	codeFileName := getCodeFileName(language)
	codeFilePath := filepath.Join(tempDir, codeFileName)
	inputFileName := "input.txt"                           // Input fayl nomi
	inputFilePath := filepath.Join(tempDir, inputFileName) // Hostdagi to'liq yo'l

	// Kod faylini yozish
	if err := ioutil.WriteFile(codeFilePath, []byte(code), 0644); err != nil {
		log.Printf("Kod faylini yozishda xato (Test ID: %d): %v", testID, err)
		testResult.Status = "Internal Error"
		testResult.Error = fmt.Sprintf("Kod faylini yozishda xato: %v", err)
		return testResult
	}
	// Input faylini yozish (agar input mavjud bo'lsa)
	if input != "" {
		if err := ioutil.WriteFile(inputFilePath, []byte(input), 0644); err != nil {
			log.Printf("Input faylini yozishda xato (Test ID: %d): %v", testID, err)
			testResult.Status = "Internal Error"
			testResult.Error = fmt.Sprintf("Input faylini yozishda xato: %v", err)
			return testResult
		}
	}

	dockerImage := getDockerImage(language)

	// Konteyner ichidagi input fayl yo'lini aniqlash
	containerInputFilePath := ""
	if input != "" { // Faqat input mavjud bo'lsa, yo'lni belgilaymiz
		containerInputFilePath = "/app/" + inputFileName // Konteyner ichidagi to'liq yo'l
	}
	// getRunCommand funksiyasiga endi konteyner ichidagi input fayl yo'lini uzatamiz
	runCommand := getRunCommand(language, codeFileName, containerInputFilePath)

	cmdArgs := []string{
		"run", "--rm",
		"--network=none",
		fmt.Sprintf("--memory=%dm", memoryMb),
		fmt.Sprintf("--memory-swap=%dm", memoryMb),
		fmt.Sprintf("--cpu-shares=%d", cpuShares),
		"-v", fmt.Sprintf("%s:/app", tempDir), // Hostdagi tempDir ni konteynerdagi /app ga mount qilamiz
		"--pids-limit=100",
		"--security-opt=no-new-privileges",
		"--cap-drop=ALL",
		dockerImage,
	}
	cmdArgs = append(cmdArgs, runCommand...)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", cmdArgs...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startTime := time.Now()
	cmdErr := cmd.Run()
	endTime := time.Now()

	testResult.TimeMs = endTime.Sub(startTime).Milliseconds()
	testResult.Actual = strings.TrimSpace(stdout.String())
	testResult.Error = stderr.String()

	if ctx.Err() == context.DeadlineExceeded {
		testResult.Status = "Time Limit Exceeded"
	} else if cmdErr != nil {
		if _, ok := cmdErr.(*exec.ExitError); ok {
			if language == "java" || language == "cpp" || language == "go" {
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
			log.Printf("Docker buyrug'ini bajarishda kutilmagan xato (Test ID: %d): %v, stderr: %s", testID, cmdErr, stderr.String())
			testResult.Status = "Internal Error"
			testResult.Error = fmt.Sprintf("Docker buyrug'ini bajarishda kutilmagan xato: %v", cmdErr)
		}
	} else {
		// Agar expectedOutput mavjud bo'lsa, solishtirish
		if expectedOutput != "" {
			trimmedExpected := strings.TrimSpace(expectedOutput)
			if testResult.Actual == trimmedExpected {
				testResult.Status = "Accepted"
				testResult.IsCorrect = true
			} else {
				testResult.Status = "Wrong Answer"
			}
		} else {
			// Custom input holatida faqat ijro etildi deb hisoblaymiz
			testResult.Status = "Executed"
			testResult.IsCorrect = true // Bu yerda "to'g'ri" degani, kod xatosiz bajarildi degani
		}
	}
	return testResult
}

// ExecuteCode - Asosiy bajarish funksiyasi. So'rov turini aniqlaydi va tegishli logikani chaqiradi.
func ExecuteCode(req ExecutionRequest) ExecutionResult {
	overallResult := ExecutionResult{
		ProblemID:     req.ProblemID, // ProblemID ni natijaga qo'shish
		OverallStatus: "Processing",
		TestResults:   []IndividualTestResult{},
		PassedTests:   0,
	}

	var testCasesToExecute []TestCase
	var err error

	if req.ProblemID != 0 {
		// Problem ID asosida test case'larni ma'lumotlar bazasidan olish
		testCasesToExecute, err = NeonDB(req.ProblemID)
		if err != nil {
			log.Printf("Testcase'larni olishda xato (Problem ID: %d): %v", req.ProblemID, err)
			overallResult.OverallStatus = "Problem Not Found or DB Error"
			overallResult.Error = fmt.Sprintf("Testcase'larni ma'lumotlar bazasidan olishda xato: %v", err)
			return overallResult
		}
		log.Printf("Problem ID %d uchun %d ta testcase topildi", req.ProblemID, len(testCasesToExecute))
	} else if req.CustomInput != "" {
		// Custom input bilan faqat bitta test case yaratish
		testCasesToExecute = []TestCase{
			{ID: 0, InputText: req.CustomInput, OutputText: ""}, // Custom inputda expected output bo'lmaydi
		}
		log.Printf("Custom input bilan kod bajarilmoqda.")
	} else if len(req.TestCases) > 0 {
		// Manual test case'lar to'g'ridan-to'g'ri so'rovdan olinadi
		testCasesToExecute = req.TestCases
		log.Printf("Manual test case'lar bilan kod bajarilmoqda. %d ta testcase.", len(testCasesToExecute))
	} else {
		// Hech qanday test turi aniqlanmagan
		overallResult.OverallStatus = "Error"
		overallResult.Error = "Test qilish uchun hech qanday Problem ID, Custom Input yoki Test Case'lar berilmagan."
		return overallResult
	}

	overallResult.TotalTests = len(testCasesToExecute)

	if len(testCasesToExecute) == 0 {
		overallResult.OverallStatus = "No Test Cases Found"
		overallResult.Error = "Bajarish uchun test case'lar topilmadi."
		return overallResult
	}

	// Test case'larni parallel bajarish
	var wg sync.WaitGroup
	resultsChan := make(chan IndividualTestResult, len(testCasesToExecute))

	for i, tc := range testCasesToExecute {
		wg.Add(1)
		// Goroutine ichida tc va i ni o'zgaruvchi sifatida uzatish, chunki tsikl tez aylanadi
		go func(testCase TestCase, index int) {
			defer wg.Done()
			// Agar custom input bo'lsa, ID 0 bo'lishi mumkin, shuning uchun noyob ID yaratish
			currentTestID := testCase.ID
			if currentTestID == 0 {
				currentTestID = index + 1 // Yoki uuid.New().ID() kabi noyob ID
			}
			res := executeSingleTestCase(req.Code, req.Language, testCase.InputText, req.TimeoutMs, req.MemoryMb, req.CpuShares, currentTestID, testCase.OutputText)
			resultsChan <- res
		}(tc, i)
	}

	wg.Wait()
	close(resultsChan) // Channelni yopish

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

	// Umumiy statusni aniqlash
	if allTestsPassed {
		overallResult.OverallStatus = "Accepted"
	} else {
		// Birinchi xatoga qarab umumiy statusni belgilash
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

// getRunCommand - tilga qarab kodni bajarish uchun Docker ichidagi buyruqni qaytaradi
// containerInputFilePath endi konteyner ichidagi to'liq yo'l bo'lishi kerak
func getRunCommand(lang, codeFileName, containerInputFilePath string) []string {
	inputRedirect := ""
	// Faqat input fayl yo'li berilgan bo'lsa, uni yo'naltiramiz
	if containerInputFilePath != "" {
		inputRedirect = fmt.Sprintf("< %s", containerInputFilePath)
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
