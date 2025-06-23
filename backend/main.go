package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/rs/cors"
)

type Peer struct {
	Name     string    `json:"name"`
	IP       string    `json:"ip"`
	Port     string    `json:"port"`
	LastSeen time.Time `json:"lastSeen"`
	Online   bool      `json:"online"`
}

type FileTransfer struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	From     string `json:"from"`
	To       string `json:"to"`
	Status   string `json:"status"` // pending, accepted, rejected, completed
}

type Hub struct {
	clients    map[*websocket.Conn]bool
	broadcast  chan []byte
	register   chan *websocket.Conn
	unregister chan *websocket.Conn
}

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	hub = &Hub{
		clients:    make(map[*websocket.Conn]bool),
		broadcast:  make(chan []byte),
		register:   make(chan *websocket.Conn),
		unregister: make(chan *websocket.Conn),
	}

	peers      = make(map[string]*Peer)
	transfers  = make(map[string]*FileTransfer)
	deviceName string
	serverPort = "8080"
)

func init() {
	user, err := user.Current()
	if err != nil {
		deviceName = "Unknown Device"
	} else {
		hostname, _ := os.Hostname()
		deviceName = fmt.Sprintf("%s@%s", user.Username, hostname)
	}
}

func main() {
	// Start WebSocket hub
	go hub.run()

	// Start peer discovery
	go startPeerDiscovery()
	go startDiscoveryBroadcast()

	// Setup routes
	r := mux.NewRouter()

	// WebSocket endpoint
	r.HandleFunc("/ws", handleWebSocket)

	// API endpoints
	r.HandleFunc("/api/peers", getPeers).Methods("GET")
	r.HandleFunc("/api/send", sendFile).Methods("POST")
	r.HandleFunc("/api/receive/{transferId}", receiveFile).Methods("GET")
	r.HandleFunc("/api/accept/{transferId}", acceptTransfer).Methods("POST")
	r.HandleFunc("/api/reject/{transferId}", rejectTransfer).Methods("POST")
	r.HandleFunc("/api/notify-transfer", notifyTransfer).Methods("POST")
	r.HandleFunc("/api/device-name", getDeviceName).Methods("GET")
	r.HandleFunc("/api/upload", receiveIncomingFile).Methods("POST")

	// Discovery endpoint
	r.HandleFunc("/discover", handleDiscovery).Methods("GET")

	// Setup CORS
	c := cors.New(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"*"},
	})

	handler := c.Handler(r)

	fmt.Printf("Server starting on port %s\n", serverPort)
	fmt.Printf("Device name: %s\n", deviceName)
	log.Fatal(http.ListenAndServe(":"+serverPort, handler))
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.Close()
			}

		case message := <-h.broadcast:
			for client := range h.clients {
				err := client.WriteMessage(websocket.TextMessage, message)
				if err != nil {
					delete(h.clients, client)
					client.Close()
				}
			}
		}
	}
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}

	hub.register <- conn

	go func() {
		defer func() {
			hub.unregister <- conn
		}()

		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}()
}

func broadcastMessage(msgType string, data interface{}) {
	message := map[string]interface{}{
		"type": msgType,
		"data": data,
	}

	jsonData, err := json.Marshal(message)
	if err != nil {
		log.Println("Error marshaling message:", err)
		return
	}

	hub.broadcast <- jsonData
}

func getLocalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

func startDiscoveryBroadcast() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			broadcastDiscovery()
		}
	}
}

func broadcastDiscovery() {
	// Get local network interfaces
	interfaces, err := net.Interfaces()
	if err != nil {
		return
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
				continue
			}

			// Broadcast to subnet
			broadcast := make(net.IP, 4)
			for i := range ipnet.IP.To4() {
				broadcast[i] = ipnet.IP.To4()[i] | ^ipnet.Mask[i]
			}

			go sendDiscoveryPacket(broadcast.String())
		}
	}
}

func sendDiscoveryPacket(broadcastIP string) {
	// Simple HTTP-based discovery
	client := &http.Client{Timeout: 2 * time.Second}

	// Try common ports in the subnet
	baseIP := broadcastIP[:strings.LastIndex(broadcastIP, ".")]
	for i := 1; i < 255; i++ {
		targetIP := fmt.Sprintf("%s.%d", baseIP, i)
		if targetIP == getLocalIP() {
			continue
		}

		go func(ip string) {
			resp, err := client.Get(fmt.Sprintf("http://%s:%s/discover", ip, serverPort))
			if err != nil {
				return
			}
			defer resp.Body.Close()

			var peer Peer
			if err := json.NewDecoder(resp.Body).Decode(&peer); err != nil {
				return
			}

			peer.IP = ip
			peer.LastSeen = time.Now()
			peer.Online = true

			peers[peer.IP] = &peer
			broadcastMessage("peer_discovered", peer)
		}(targetIP)
	}
}

func startPeerDiscovery() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Clean up offline peers
			for ip, peer := range peers {
				if time.Since(peer.LastSeen) > 60*time.Second {
					peer.Online = false
					broadcastMessage("peer_offline", peer)
				}
				if time.Since(peer.LastSeen) > 300*time.Second {
					delete(peers, ip)
				}
			}
		}
	}
}

func handleDiscovery(w http.ResponseWriter, r *http.Request) {
	peer := Peer{
		Name: deviceName,
		IP:   getLocalIP(),
		Port: serverPort,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(peer)
}

func getPeers(w http.ResponseWriter, r *http.Request) {
	peerList := make([]*Peer, 0, len(peers))
	for _, peer := range peers {
		if peer.Online {
			peerList = append(peerList, peer)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(peerList)
}

func getDeviceName(w http.ResponseWriter, r *http.Request) {
	response := map[string]string{"name": deviceName}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func sendFile(w http.ResponseWriter, r *http.Request) {
	err := r.ParseMultipartForm(32 << 20) // 32MB max
	if err != nil {
		http.Error(w, "Error parsing form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error getting file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	targetIP := r.FormValue("targetIP")
	if targetIP == "" {
		http.Error(w, "Target IP required", http.StatusBadRequest)
		return
	}

	// Create transfer record
	transferID := fmt.Sprintf("%d", time.Now().UnixNano())
	transfer := &FileTransfer{
		ID:       transferID,
		Filename: header.Filename,
		Size:     header.Size,
		From:     deviceName,
		To:       targetIP,
		Status:   "pending",
	}

	transfers[transferID] = transfer

	// Save file temporarily
	tempDir := filepath.Join(os.TempDir(), "file-share")
	os.MkdirAll(tempDir, 0o755)

	tempPath := filepath.Join(tempDir, transferID+"_"+header.Filename)
	tempFile, err := os.Create(tempPath)
	if err != nil {
		http.Error(w, "Error creating temp file", http.StatusInternalServerError)
		return
	}
	defer tempFile.Close()

	io.Copy(tempFile, file)

	// Notify target peer
	go notifyPeerOfTransfer(targetIP, transfer)

	go uploadFileToPeer(tempPath, targetIP, transferID, header.Filename)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(transfer)
}

func notifyTransfer(w http.ResponseWriter, r *http.Request) {
	var transfer FileTransfer
	if err := json.NewDecoder(r.Body).Decode(&transfer); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	transfers[transfer.ID] = &transfer
	broadcastMessage("transfer_request", transfer)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "notified"})
}

func notifyPeerOfTransfer(targetIP string, transfer *FileTransfer) {
	client := &http.Client{Timeout: 10 * time.Second}

	jsonData, _ := json.Marshal(transfer)
	resp, err := client.Post(
		fmt.Sprintf("http://%s:%s/api/notify-transfer", targetIP, serverPort),
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		log.Printf("Error notifying peer %s: %v", targetIP, err)
		return
	}
	defer resp.Body.Close()
}

func uploadFileToPeer(filePath, targetIP, transferID, filename string) {
	file, err := os.Open(filePath)
	if err != nil {
		log.Println("Failed to open file for upload:", err)
		return
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := io.MultiWriter(body)
	form := multipart.NewWriter(body)

	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		log.Println("Failed to create form file:", err)
		return
	}
	io.Copy(part, file)
	form.WriteField("transferId", transferID)
	form.Close()

	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s:%s/api/upload", targetIP, serverPort), body)
	if err != nil {
		log.Println("Failed to create upload request:", err)
		return
	}
	req.Header.Set("Content-Type", form.FormDataContentType())

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Println("Failed to upload file to peer:", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Println("Peer upload failed with status:", resp.Status)
	}
}

func receiveFile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	transferID := vars["transferId"]

	transfer, exists := transfers[transferID]
	if !exists {
		http.Error(w, "Transfer not found", http.StatusNotFound)
		return
	}

	if transfer.Status != "accepted" {
		http.Error(w, "Transfer not accepted", http.StatusBadRequest)
		return
	}

	tempDir := filepath.Join(os.TempDir(), "file-share")
	tempPath := filepath.Join(tempDir, transferID+"_"+transfer.Filename)

	file, err := os.Open(tempPath)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", transfer.Filename))
	w.Header().Set("Content-Type", "application/octet-stream")

	io.Copy(w, file)

	// Clean up
	os.Remove(tempPath)
	transfer.Status = "completed"
	broadcastMessage("transfer_completed", transfer)
}

func acceptTransfer(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	transferID := vars["transferId"]

	transfer, exists := transfers[transferID]
	if !exists {
		http.Error(w, "Transfer not found", http.StatusNotFound)
		return
	}

	transfer.Status = "accepted"
	broadcastMessage("transfer_accepted", transfer)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(transfer)
}

func rejectTransfer(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	transferID := vars["transferId"]

	transfer, exists := transfers[transferID]
	if !exists {
		http.Error(w, "Transfer not found", http.StatusNotFound)
		return
	}

	transfer.Status = "rejected"
	broadcastMessage("transfer_rejected", transfer)

	// Clean up temp file
	tempDir := filepath.Join(os.TempDir(), "file-share")
	tempPath := filepath.Join(tempDir, transferID+"_"+transfer.Filename)
	os.Remove(tempPath)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(transfer)
}

// Receiver handler
func receiveIncomingFile(w http.ResponseWriter, r *http.Request) {
	transferID := r.FormValue("transferId")

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to read file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	tempDir := filepath.Join(os.TempDir(), "file-share")
	os.MkdirAll(tempDir, 0o755)

	tempPath := filepath.Join(tempDir, transferID+"_"+header.Filename)
	out, err := os.Create(tempPath)
	if err != nil {
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}
	defer out.Close()

	io.Copy(out, file)
	w.WriteHeader(http.StatusOK)
}
