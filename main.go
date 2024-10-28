package main

import (
    "bufio"
    "fmt"
    "log"
    "net/http"
    "os"
    "strings"
    "time"
    "github.com/joho/godotenv"
    "encoding/json"
    "bytes"
)

var (
    previousMetrics = make(map[string]int) // 메트릭의 이전 값을 저장
)
type MetricFilter struct {
    Name string
    Filter []string
    AlertFunc func(string, int) // 각 메트릭에 대한 알림 조건 및 처리 함수
}

func hcHandler(w http.ResponseWriter, r *http.Request) {
    log.Printf("Received request from %s", r.RemoteAddr)

    // 상태 코드 200 설정
    w.WriteHeader(http.StatusOK)
    // 응답 메시지로 "OK" 전송
    fmt.Fprintln(w, "OK")
}

func main() {
    http.HandleFunc("/health", hcHandler)

    // 서버 포트 설정
    port := "6262"
    log.Printf("Server starting on port %s\n", port)

    // 서버 시작
    if err := http.ListenAndServe(":"+port, nil); err != nil {
        log.Fatalf("Failed to start server: %v\n", err)
    }
    // 주기적으로 메트릭을 확인하는 로직 (예: 1분마다)
    ticker := time.NewTicker(10 * time.Minute)
    defer ticker.Stop()

    metrics := []MetricFilter{
        {Name: "uptime", Filter: []string{`process="bridge"`}, AlertFunc: uptimeAlert},
        {Name: "bridge_requests_ok", Filter: []string{`type="handle_eth_tx_hash"`}, AlertFunc: requestsOkAlert},
        {Name: "bridge_requests_ok", Filter: []string{`type="handle_sui_tx_digest"`}, AlertFunc: requestsOkAlert},
        {Name: "bridge_requests_received", Filter: []string{`type="handle_eth_tx_hash"`}, AlertFunc: requestsOkAlert},
        {Name: "bridge_requests_received", Filter: []string{`type="handle_sui_tx_digest"`}, AlertFunc: requestsOkAlert},
    }

    // 프로그램 시작 시 즉시 메트릭 확인
    log.Println("Fetching metrics at startup...")
    fetchMetrics(metrics)

    log.Println("Monitoring metrics every 1 minute...")
    for range ticker.C {
        fetchMetrics(metrics)
    }
}

func fetchMetrics(metricFilters []MetricFilter) {
    // Prometheus /metrics 엔드포인트에서 메트릭을 가져오는 HTTP 요청
    resp, err := http.Get("http://51.195.61.7:9183/metrics")
    if err != nil {
        log.Printf("Error fetching metrics: %v\n", err)
        return
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        log.Printf("Error: received non-200 status code: %d\n", resp.StatusCode)
        // 호출에러로 인한 slack 알람 추가 예정
        return
    }

    // HTTP 응답 바디를 읽고 필요한 메트릭만 필터링
    scanner := bufio.NewScanner(resp.Body)
    for scanner.Scan() {
        line := scanner.Text()
        for _, metric := range metricFilters {
            if strings.Contains(line, metric.Name) && matchesFilters(line, metric.Filter){
                value, err := extractValueFromLine(line)
                if err != nil {
                    log.Printf("Error extracting value: %v\n", err)
                    continue
                }
                metric.AlertFunc(metric.Name, value)
                log.Printf("%s: %s %d\n", metric.Name, metric.Filter, value)
            }
        }
    }

    if err := scanner.Err(); err != nil {
        log.Printf("Error reading response body: %v\n", err)
    }
}
func matchesFilters(line string, filters []string) bool {
    for _, filter := range filters {
        if !strings.Contains(line, filter) {
            return false
        }
    }
    return true
}
// func checkForAlert(metric, line string) {
//     currentValue, err := extractValueFromLine(line)
//     if err != nil {
//         log.Printf("Error extracting value for %s: %v\n", metric, err)
//         return
//     }

//     // 이전 값과 비교
//     previousValue, exists := previousMetrics[metric]
//     if exists && currentValue == previousValue {
//         log.Printf("Error: Metric %s has not changed in the last minute. Current value: %d\n", metric, currentValue)
//         sendAlert(fmt.Sprintf("Warning: Metric %s has not changed. Current value: %d", metric, currentValue))
//     }

//     // 이전 값을 현재 값으로 업데이트
//     previousMetrics[metric] = currentValue
// }
func uptimeAlert(metric string, currentValue int) {
    previousValue, exists := previousMetrics[metric]
    if exists {
        if currentValue == previousValue {
            sendAlert(fmt.Sprintf("Warning: %s has not changed. Current value: %d", metric, currentValue))
            fmt.Println("증가하지않음.얼러트 조건 완성")
        } else if currentValue < 1000 {
            sendAlert(fmt.Sprintf("Critical: %s seems to have restarted. Current value: %d", metric, currentValue))
            fmt.Printf("Critical: %s seems to have restarted. Current value: %d", metric, currentValue)
        }
    }
    previousMetrics[metric] = currentValue
}

func requestsOkAlert(metric string, currentValue int) {
    previousValue, exists := previousMetrics[metric]
    currentUptime, uptimeExists := previousMetrics["uptime"]

    if exists && currentValue == previousValue {
        // Uptime 메트릭을 확인
        if uptimeExists {
            previousUptime := previousMetrics["previous_uptime"]
            if currentUptime > previousUptime {
                // Uptime이 증가했다면 시스템은 정상 작동 중이므로 경고를 보내지 않음
                fmt.Println("No alert sent. System is operating normally.")
            }
        }
    }
    previousMetrics[metric] = currentValue
    previousMetrics["previous_uptime"] = currentUptime
}

func extractValueFromLine(line string) (int, error) {
    parts := strings.Fields(line)
    if len(parts) < 2 {
        return 0, fmt.Errorf("invalid metric line: %s", line)
    }
    var value int
    _, err := fmt.Sscanf(parts[len(parts)-1], "%d", &value)
    if err != nil {
        return 0, err
    }
    return value, nil
}

// Slack으로 알림을 전송하는 함수
func sendAlert(message string) {
    err := godotenv.Load()
    webhookUrl := os.Getenv("SLACK_WEBHOOK_URL")
    if webhookUrl == "" {
        log.Println("SLACK_WEBHOOK_URL is not set. Skipping alert.")
        return
    }

    payload := fmt.Sprintf(`{"text": "[Sui-Bridge] \n%s"}`, message)
    resp, err := http.Post(webhookUrl, "application/json", strings.NewReader(payload))
    if err != nil {
        log.Printf("Error sending alert to Slack: %v\n", err)
        return
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        log.Printf("Error: received non-200 status code from Slack: %d\n", resp.StatusCode)
    }
    callPd(message)
}

func callPd(message string) {
    err := godotenv.Load()
    if err != nil {
        log.Printf("Error loading .env file: %v\n", err)
        return
    }
    routingKey := os.Getenv("PAGERDUTY_ROUTING_KEY")
    if routingKey == "" {
        log.Println("PAGERDUTY_ROUTING_KEY is not set. Skipping PagerDuty alert.")
        return
    }

    url := "https://events.pagerduty.com/v2/enqueue"
    payload := map[string]interface{}{
        "routing_key": routingKey,
        "event_action": "trigger",
        "payload": map[string]interface{}{
            "summary":  "Sui Bridge" + message,
            "source":   "Sui Bridge",
            "severity": "critical",
        },
    }
    data, err := json.Marshal(payload)
    if err != nil {
        log.Printf("Failed to marshal PagerDuty payload: %v\n", err)
        return
    }

    resp, err := http.Post(url, "application/json", bytes.NewBuffer(data))
    if err != nil {
        log.Printf("Error sending alert to PagerDuty: %v\n", err)
        return
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusAccepted {
        log.Printf("PagerDuty API returned non-accepted status code: %d\n", resp.StatusCode)
    } else {
        log.Println("PagerDuty alert sent successfully")
    }
}