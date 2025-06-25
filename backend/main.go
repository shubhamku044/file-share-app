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
	FromIP   string `json:"fromIP"`
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
	r.HandleFunc("/api/download/{transferId}", downloadFile).Methods("GET")
	r.HandleFunc("/api/accept/{transferId}", acceptTransfer).Methods("POST")
	r.HandleFunc("/api/accept-remote/{transferId}", acceptRemoteTransfer).Methods("POST")
	r.HandleFunc("/api/reject/{transferId}", rejectTransfer).Methods("POST")
	r.HandleFunc("/api/notify-transfer", notifyTransfer).Methods("POST")
	r.HandleFunc("/api/device-name", getDeviceName).Methods("GET")
	r.HandleFunc("/api/upload/{transferId}", receiveFileFromSender).Methods("POST")

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
		FromIP:   getLocalIP(),
	}

	transfers[transferID] = transfer

	// Save file temporarily on sender side
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

	// Notify target peer about the transfer
	go notifyPeerOfTransfer(targetIP, transfer)

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

	// Notify sender to start file transfer
	if transfer.FromIP != "" {
		go notifySenderOfAcceptance(transfer.FromIP, transfer.ID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(transfer)
}

func notifySenderOfAcceptance(senderIP, transferID string) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(
		fmt.Sprintf("http://%s:%s/api/accept-remote/%s", senderIP, serverPort, transferID),
		"application/json",
		nil,
	)
	if err != nil {
		log.Printf("Error notifying sender: %v", err)
		return
	}
	defer resp.Body.Close()
}

func requestFileFromSender(transfer *FileTransfer) {
	// Find sender's IP from the transfer.From field
	// We need to extract IP from device name or use a mapping
	// For now, let's assume transfer.From contains the IP or we find it in peers
	var senderIP string
	for ip, peer := range peers {
		if peer.Name == transfer.From {
			senderIP = ip
			break
		}
	}

	if senderIP == "" {
		log.Printf("Could not find sender IP for transfer %s", transfer.ID)
		return
	}

	// Request the file from sender
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:%s/api/upload/%s", senderIP, serverPort, transfer.ID))
	if err != nil {
		log.Printf("Error requesting file from sender: %v", err)
		return
	}
	defer resp.Body.Close()

	log.Printf("Requested file from sender %s for transfer %s", senderIP, transfer.ID)
}

func receiveFileFromSender(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	transferID := vars["transferId"]

	transfer, exists := transfers[transferID]
	if !exists {
		// This might be a cross-device transfer, try to get file from sender
		log.Printf("Transfer %s not found locally, attempting to fetch from sender", transferID)

		// Parse the form to get file
		err := r.ParseMultipartForm(32 << 20)
		if err != nil {
			http.Error(w, "Failed to parse form", http.StatusBadRequest)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "File not found in form", http.StatusBadRequest)
			return
		}
		defer file.Close()

		// Save the file
		tempDir := filepath.Join(os.TempDir(), "file-share")
		os.MkdirAll(tempDir, 0o755)
		tempPath := filepath.Join(tempDir, transferID+"_"+header.Filename)

		out, err := os.Create(tempPath)
		if err != nil {
			http.Error(w, "Failed to create file", http.StatusInternalServerError)
			return
		}
		defer out.Close()

		io.Copy(out, file)

		// Create transfer record if it doesn't exist
		transfer = &FileTransfer{
			ID:       transferID,
			Filename: header.Filename,
			Size:     header.Size,
			From:     r.FormValue("from"),
			To:       deviceName,
			Status:   "completed",
		}
		transfers[transferID] = transfer

		broadcastMessage("transfer_completed", transfer)
		log.Printf("File received and saved for transfer %s", transferID)

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "received"})
		return
	}

	// If transfer exists, this is the sender uploading the file
	if transfer.Status != "accepted" {
		http.Error(w, "Transfer not accepted", http.StatusBadRequest)
		return
	}

	err := r.ParseMultipartForm(32 << 20)
	if err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		// This endpoint was called by receiver to get file from sender
		log.Printf("Sender serving file for transfer %s", transferID)

		tempDir := filepath.Join(os.TempDir(), "file-share")
		tempPath := filepath.Join(tempDir, transferID+"_"+transfer.Filename)

		fileData, err := os.Open(tempPath)
		if err != nil {
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		defer fileData.Close()

		// Upload file to receiver
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, err := writer.CreateFormFile("file", transfer.Filename)
		if err != nil {
			log.Println("Error creating form file:", err)
			return
		}
		io.Copy(part, fileData)
		writer.WriteField("from", transfer.From)
		writer.Close()

		// Send to receiver
		receiverIP := transfer.To
		uploadURL := fmt.Sprintf("http://%s:%s/api/upload/%s", receiverIP, serverPort, transfer.ID)
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Post(uploadURL, writer.FormDataContentType(), body)
		if err != nil {
			log.Printf("Error uploading file to receiver: %v", err)
			http.Error(w, "Failed to upload to receiver", http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			transfer.Status = "completed"
			broadcastMessage("transfer_completed", transfer)
			log.Printf("File successfully sent to receiver for transfer %s", transferID)
		}

		w.WriteHeader(http.StatusOK)
		return
	}
	defer file.Close()

	// Save received file
	tempDir := filepath.Join(os.TempDir(), "file-share")
	os.MkdirAll(tempDir, 0o755)
	tempPath := filepath.Join(tempDir, transferID+"_"+header.Filename)

	out, err := os.Create(tempPath)
	if err != nil {
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return
	}
	defer out.Close()

	io.Copy(out, file)

	transfer.Status = "completed"
	broadcastMessage("transfer_completed", transfer)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "uploaded"})
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

func downloadFile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	transferID := vars["transferId"]

	transfer, exists := transfers[transferID]
	if !exists {
		http.Error(w, "Transfer not found", http.StatusNotFound)
		return
	}

	if transfer.Status != "completed" {
		http.Error(w, "Transfer not completed", http.StatusBadRequest)
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

	// Clean up after download
	os.Remove(tempPath)
	log.Printf("File downloaded and cleaned up for transfer %s", transferID)
}

func pushFileToReceiver(transfer *FileTransfer) {
	tempDir := filepath.Join(os.TempDir(), "file-share")
	tempPath := filepath.Join(tempDir, transfer.ID+"_"+transfer.Filename)

	fileData, err := os.Open(tempPath)
	if err != nil {
		log.Printf("Error opening file: %v", err)
		return
	}
	defer fileData.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", transfer.Filename)
	if err != nil {
		log.Println("Error creating form file:", err)
		return
	}
	io.Copy(part, fileData)
	writer.Close()

	// Send to receiver
	uploadURL := fmt.Sprintf("http://%s:%s/api/upload/%s", transfer.To, serverPort, transfer.ID)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(uploadURL, writer.FormDataContentType(), body)
	if err != nil {
		log.Printf("Error uploading file to receiver: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		transfer.Status = "completed"
		broadcastMessage("transfer_completed", transfer)
	}
}

func acceptRemoteTransfer(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	transferID := vars["transferId"]

	transfer, exists := transfers[transferID]
	if !exists {
		http.Error(w, "Transfer not found", http.StatusNotFound)
		return
	}

	transfer.Status = "accepted"
	broadcastMessage("transfer_accepted", transfer)

	// Start pushing file to receiver
	go pushFileToReceiver(transfer)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(transfer)
}
