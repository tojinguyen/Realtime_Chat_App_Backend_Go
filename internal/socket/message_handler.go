package socket

import (
	"context"
	"encoding/json"
	domain "gochat-backend/internal/domain/chat"
	"gochat-backend/internal/repository"
	"log"
	"time"

	"github.com/google/uuid"
)

type MessageHandler struct {
	hub                *Hub
	chatRoomRepository repository.ChatRoomRepository
	messageRepository  repository.MessageRepository
	accountRepository  repository.AccountRepository
}

func NewMessageHandler(
	hub *Hub,
	chatRoomRepository repository.ChatRoomRepository,
	messageRepository repository.MessageRepository,
	accountRepository repository.AccountRepository,
) *MessageHandler {
	return &MessageHandler{
		hub:                hub,
		chatRoomRepository: chatRoomRepository,
		messageRepository:  messageRepository,
		accountRepository:  accountRepository,
	}
}

// HandleSocketMessage xử lý tin nhắn từ client
// HandleSocketMessageWithContext xử lý tin nhắn từ client với context
func (h *MessageHandler) HandleSocketMessageWithContext(client *Client, data []byte, ctx context.Context) {
	var socketMsg SocketMessage
	if err := json.Unmarshal(data, &socketMsg); err != nil {
		h.sendErrorToClient(client, "Message format is invalid")
		log.Printf("Error parsing message: %v", err)
		return
	}

	// Ensure sender ID and timestamp
	socketMsg.SenderID = client.ID
	socketMsg.Timestamp = time.Now().UnixMilli()

	log.Printf("Received message from client %s: %s", client.ID, string(data))

	// Check if context is canceled
	select {
	case <-ctx.Done():
		log.Printf("Context canceled while processing message from client %s", client.ID)
		return
	default:
		// Continue processing
	}

	switch socketMsg.Type {
	case SocketMessageTypeChat:
		h.handleChatMessage(client, socketMsg, ctx)
	case SocketMessageTypeJoin:
		h.handleJoinMessage(client, socketMsg)
	case SocketMessageTypeLeave:
		h.handleLeaveMessage(client, socketMsg)
	case SocketMessageTypeTyping:
		h.handleTypingMessage(client, socketMsg)
	case SocketMessageTypeReadReceipt:
		h.handleReadReceiptMessage(client, socketMsg)
	default:
		h.sendErrorToClient(client, "Unknown message type")
	}
}

// handleChatMessage xử lý tin nhắn chat
func (h *MessageHandler) handleChatMessage(client *Client, socketMsg SocketMessage, ctx context.Context) {
	log.Printf("Handling chat message from client %s", client.ID)

	if !h.hub.IsClientInRoom(socketMsg.ChatRoomID, client.ID) {
		log.Printf("Client %s is not in room %s", client.ID, socketMsg.ChatRoomID)
		h.sendErrorToClient(client, "You haven't joined this room")
		return
	}

	// Parse payload từ Data
	payload, err := ParsePayload[ChatMessagePayload](socketMsg.Data)
	if err != nil {
		log.Printf("Error parsing chat message payload: %v", err)
		h.sendErrorToClient(client, "Invalid payload format")
		return
	}

	if CheckContext(ctx, client.ID, "Context canceled during chat message processing") {
		log.Printf("Context canceled while processing chat message from client %s", client.ID)
		return
	}

	message := &domain.Message{
		ID:         uuid.New().String(), // Hàm tạo ID duy nhất (bạn cần định nghĩa)
		SenderId:   socketMsg.SenderID,
		ChatRoomId: socketMsg.ChatRoomID,
		Type:       domain.TextMessageType, // Hoặc loại tin nhắn phù hợp
		MimeType:   payload.MimeType,
		Content:    payload.Content,
		CreatedAt:  time.Now().UTC(), // Thời gian hiện tại
	}

	err = h.messageRepository.CreateMessage(ctx, message)

	if err != nil {
		if CheckContext(ctx, client.ID, "Context canceled during chat message processing") {
			log.Printf("Context canceled while processing chat message from client %s", client.ID)
			return
		}
		log.Printf("Error saving message to database: %v", err)
		h.sendErrorToClient(client, "Không thể lưu tin nhắn")
		return
	}

	if CheckContext(ctx, client.ID, "Context canceled before broadcasting") {
		log.Printf("Context canceled while broadcasting message from client %s", client.ID)
		return
	}

	log.Printf("Broadcasting message to room %s", socketMsg.ChatRoomID)
	h.hub.BroadcastToRoom(socketMsg.ChatRoomID, socketMsg)
}

// handleJoinMessage xử lý yêu cầu tham gia phòng
func (h *MessageHandler) handleJoinMessage(client *Client, socketMsg SocketMessage) {
	payload, err := ParsePayload[JoinPayload](socketMsg.Data)

	if err != nil {
		log.Printf("Error parsing join message payload: %v", err)
		h.sendErrorToClient(client, "Invalid payload format")
		return
	}

	if payload.RoomID == "" {
		log.Printf("Client %s sent join message without room ID", client.ID)
		h.sendErrorToClient(client, "Thiếu thông tin phòng chat")
		return
	}

	// Kiểm tra quyền tham gia phòng (có thể thêm logic ở đây)
	// ...

	// Tham gia phòng
	h.hub.JoinRoomWithResponse(socketMsg.ChatRoomID, client)
}

// handleLeaveMessage xử lý yêu cầu rời phòng
func (h *MessageHandler) handleLeaveMessage(client *Client, socketMsg SocketMessage) {
	if socketMsg.ChatRoomID == "" {
		h.sendErrorToClient(client, "Thiếu thông tin phòng chat")
		return
	}
	h.hub.LeaveRoom(socketMsg.ChatRoomID, client)
}

// handleTypingMessage xử lý thông báo đang gõ
func (h *MessageHandler) handleTypingMessage(client *Client, socketMsg SocketMessage) {
	if !h.hub.IsClientInRoom(socketMsg.ChatRoomID, client.ID) {
		h.sendErrorToClient(client, "You aren't in this room")
		return // Bỏ qua nếu không ở trong phòng
	}
	h.hub.BroadcastToRoom(socketMsg.ChatRoomID, socketMsg)
}

// handleReadReceiptMessage xử lý xác nhận đã đọc
func (h *MessageHandler) handleReadReceiptMessage(client *Client, socketMsg SocketMessage) {
	if !h.hub.IsClientInRoom(socketMsg.ChatRoomID, client.ID) {
		return
	}
	h.hub.BroadcastToRoom(socketMsg.ChatRoomID, socketMsg)
}

// sendErrorToClient gửi thông báo lỗi cho client
func (h *MessageHandler) sendErrorToClient(client *Client, errorMsg string) {
	msg := SocketMessage{
		Type:      SocketMessageTypeError,
		SenderID:  "system",
		Timestamp: time.Now().UnixMilli(),
	}

	// Chuyển errorMsg thành JSON và gán vào Data
	data, _ := json.Marshal(map[string]string{"message": errorMsg})
	msg.Data = data

	messageJSON, _ := json.Marshal(msg)
	select {
	case client.Send <- messageJSON:
		// Gửi thành công
	default:
		// Không thể gửi
		log.Printf("Failed to send error message to client %s", client.ID)
	}
}
