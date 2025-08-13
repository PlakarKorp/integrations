package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	apiBase   = "https://berealapi.fly.dev"
	outputDir = "bereal_memories"
)

type SendCodeRequest struct {
	PhoneNumber string `json:"phoneNumber"`
}

type LoginRequest struct {
	OtpSession string `json:"otpSession"`
	Code       string `json:"code"`
}

type LoginResponse struct {
	Token string `json:"token"`
}

type Memory struct {
	Primary struct {
		URL     string    `json:"url"`
		TakenAt time.Time `json:"takenAt"`
	} `json:"primary"`
	Secondary struct {
		URL string `json:"url"`
	} `json:"secondary"`
}

func main() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("📱 Entrez votre numéro de téléphone (ex: +33612345678) : ")
	phone, _ := reader.ReadString('\n')
	phone = strings.TrimSpace(phone)

	// Étape 1 : envoyer le code
	fmt.Println("📨 Envoi du code...")
	if err := sendCode(phone); err != nil {
		panic(err)
	}

	fmt.Print("🔐 Entrez le code reçu par SMS : ")
	code, _ := reader.ReadString('\n')
	code = strings.TrimSpace(code)

	// Étape 2 : login
	token, err := login(phone, code)
	if err != nil {
		panic(err)
	}
	fmt.Println("✅ Authentification réussie !")

	// Étape 3 : télécharger les souvenirs
	if err := downloadMemories(token); err != nil {
		panic(err)
	}
	fmt.Println("✅ Téléchargement terminé.")
}

func sendCode(phone string) error {
	payload := SendCodeRequest{PhoneNumber: phone}
	return postJSON("/login/send-code", payload, nil)
}

func login(phone, code string) (string, error) {
	payload := LoginRequest{Code: code, OtpSession: phone}
	var res LoginResponse
	if err := postJSON("/login/verify", payload, &res); err != nil {
		return "", err
	}
	return res.Token, nil
}

func downloadMemories(token string) error {
	_ = os.MkdirAll(outputDir, 0755)

	req, _ := http.NewRequest("GET", apiBase+"/feed/memories", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("erreur API: %s\n%s", resp.Status, body)
	}

	var memories []Memory
	if err := json.NewDecoder(resp.Body).Decode(&memories); err != nil {
		return err
	}

	fmt.Printf("📸 %d BeReal trouvés.\n", len(memories))

	for _, mem := range memories {
		date := mem.Primary.TakenAt.Format("2006-01-02")
		saveImage(mem.Primary.URL, filepath.Join(outputDir, fmt.Sprintf("%s_back.jpg", date)))
		saveImage(mem.Secondary.URL, filepath.Join(outputDir, fmt.Sprintf("%s_front.jpg", date)))
	}
	return nil
}

func saveImage(url, path string) {
	resp, err := http.Get(url)
	if err != nil {
		fmt.Printf("❌ Erreur téléchargement : %s\n", url)
		return
	}
	defer resp.Body.Close()

	f, err := os.Create(path)
	if err != nil {
		fmt.Printf("❌ Erreur fichier : %s\n", path)
		return
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	if err != nil {
		fmt.Printf("❌ Erreur écriture : %s\n", path)
	}
}

func postJSON(path string, payload any, target any) error {
	data, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", apiBase+path, bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("❌ HTTP %d : %s", resp.StatusCode, string(body))
	}

	if target != nil {
		return json.NewDecoder(resp.Body).Decode(target)
	}
	return nil
}
