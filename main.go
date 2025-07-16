package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

type ServerInfo struct {
	RootUsername string   `json:"root_username"`
	RootPassword string   `json:"root_password"`
	Accounts     []string `json:"accounts"`
}

var ipMap map[string]ServerInfo

func loadIPMap() error {
	file, err := os.Open("ipmap.json")
	if err != nil {
		ipMap = make(map[string]ServerInfo)
		return nil
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	err = decoder.Decode(&ipMap)
	if err != nil {
		ipMap = make(map[string]ServerInfo) // fallback to empty map
	}
	return err
}

func saveIPMap() error {
	file, err := os.Create("ipmap.json")
	if err != nil {
		return err
	}
	defer file.Close()
	err = json.NewEncoder(file).Encode(ipMap)
	if err == nil {
		file.Sync() // ensure flush
	}
	return err
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFiles("templates/index.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	err = tmpl.Execute(w, ipMap)
	if err != nil {
		http.Error(w, "Template exec error: "+err.Error(), http.StatusInternalServerError)
	}
}

func addIPHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		ip := strings.TrimSpace(r.FormValue("ip"))
		rootUser := strings.TrimSpace(r.FormValue("root_username"))
		rootPass := strings.TrimSpace(r.FormValue("root_password"))

		ipMap[ip] = ServerInfo{
			RootUsername: rootUser,
			RootPassword: rootPass,
			Accounts:     []string{},
		}
		saveIPMap()
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func executeRemoteScript(ip, user, pass, script string) (string, error) {
	client, err := ssh.Dial("tcp", ip+":22", &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		return "", err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	var output strings.Builder
	session.Stdout = &output
	session.Stderr = &output
	session.Stdin = strings.NewReader(script)

	err = session.Run("bash -s")
	return output.String(), err
}

func uploadCSVHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFiles("templates/upload.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	err = tmpl.Execute(w, ipMap)
	if err != nil {
		http.Error(w, "Template exec error: "+err.Error(), http.StatusInternalServerError)
	}
}

func createUsersHandler(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.FormValue("server_ip"))
	file, handler, err := r.FormFile("csvfile")
	if err != nil {
		http.Error(w, "Error reading file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer file.Close()

	path := filepath.Join("uploads", handler.Filename)
	out, err := os.Create(path)
	if err != nil {
		http.Error(w, "Error saving file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	io.Copy(out, file)
	out.Close()

	server, ok := ipMap[ip]
	if !ok {
		http.Error(w, "IP not found in records", http.StatusBadRequest)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "Failed to open uploaded CSV", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	reader := csv.NewReader(f)
	_, _ = reader.Read() // skip header

	var script strings.Builder
	var created []string
	var logBuilder strings.Builder

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if len(record) < 2 {
			logBuilder.WriteString(fmt.Sprintf("❌ Skipped invalid row: %v\n", record))
			continue
		}

		username := strings.TrimSpace(record[0])
		password := strings.TrimSpace(record[1])
		if username == "" || password == "" {
			logBuilder.WriteString(fmt.Sprintf("❌ Skipped empty fields: %v\n", record))
			continue
		}

		safePass := strings.ReplaceAll(password, `'`, `'\''`)
		script.WriteString(fmt.Sprintf("sudo useradd -m -s /bin/bash %s && echo '%s:%s' | sudo chpasswd\n", username, username, safePass))
		created = append(created, username)
	}

	if len(created) == 0 {
		logBuilder.WriteString("⚠️ No valid user entries found.\n")
	}

	output, err := executeRemoteScript(ip, server.RootUsername, server.RootPassword, script.String())
	if err != nil {
		logBuilder.WriteString("❌ Remote script execution failed:\n")
	}
	logBuilder.WriteString(output)

	s := ipMap[ip]
	s.Accounts = append(s.Accounts, created...)
	ipMap[ip] = s
	saveIPMap()

	tmpl := template.Must(template.ParseFiles("templates/logs.html"))
	if err := tmpl.Execute(w, logBuilder.String()); err != nil {
		http.Error(w, "Log rendering failed: "+err.Error(), http.StatusInternalServerError)
	}
}

func main() {
	os.MkdirAll("uploads", 0755)
	ipMap = make(map[string]ServerInfo)
	loadIPMap()

	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/add-ip", addIPHandler)
	http.HandleFunc("/upload-csv", uploadCSVHandler)
	http.HandleFunc("/create-users", createUsersHandler)

	fmt.Println("🌐 Server running at http://localhost:8080")
	http.ListenAndServe(":8080", nil)
}
