package socket

import (
	"context"
	"encoding/json"
	domain "gochat-backend/internal/domain/chat"
	"gochat-backend/internal/repository"
	"log"
	"strings"
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

// HandleSocketMessageWithContext xử lý tin nhắn từ client với context
func (mh *MessageHandler) HandleSocketMessageWithContext(client *Client, data []byte, ctx context.Context) {
	var socketMsg SocketMessage
	if err := json.Unmarshal(data, &socketMsg); err != nil {
		mh.sendErrorToClient(client, "Message format is invalid", "")
		log.Printf("MH: Error parsing message: %v. Data: %s", err, string(data))
		return
	}

	// Gán SenderID và Timestamp nếu client không gửi (hoặc để ghi đè)
	socketMsg.SenderID = client.ID // Luôn dùng ID của client đã xác thực
	socketMsg.Timestamp = time.Now().UTC().UnixMilli()

	log.Printf("MH: Received message type '%s' from client %s for room '%s'", socketMsg.Type, client.ID, socketMsg.ChatRoomID)

	// Kiểm tra context trước khi xử lý
	if CheckContext(ctx, client.ID, "MH: Context canceled before message processing") {
		return
	}

	switch socketMsg.Type {
	case SocketMessageTypeChat:
		mh.handleChatMessage(client, socketMsg, ctx)
	case SocketMessageTypeJoin:
		mh.handleJoinRoomMessage(client, socketMsg, ctx)
	case SocketMessageTypeLeave:
		mh.handleLeaveRoomMessage(client, socketMsg, ctx)
	case SocketMessageTypeTyping:
		mh.handleTypingMessage(client, socketMsg, ctx)
	case SocketMessageTypeReadReceipt:
		mh.handleReadReceiptMessage(client, socketMsg, ctx)
	case SocketMessageTypePing:
		mh.sendPongToClient(client)
	default:
		log.Printf("MH: Unknown message type '%s' from client %s", socketMsg.Type, client.ID)
		mh.sendErrorToClient(client, "Unknown message type", "")
	}
}

func (mh *MessageHandler) handleChatMessage(client *Client, socketMsg SocketMessage, ctx context.Context) {
	log.Printf("MH: Handling CHAT message from client %s for room %s", client.ID, socketMsg.ChatRoomID)

	if socketMsg.ChatRoomID == "" {
		mh.sendErrorToClient(client, "ChatRoomID is required for CHAT message", "CHAT_NO_ROOM_ID")
		return
	}

	// 1. Kiểm tra client có phải là thành viên (DB) của phòng này không
	isMemberDB, err := mh.chatRoomRepository.IsUserMemberOfChatRoom(ctx, client.ID, socketMsg.ChatRoomID)
	if err != nil {
		log.Printf("MH: Error checking DB membership for client %s in room %s: %v", client.ID, socketMsg.ChatRoomID, err)
		mh.sendErrorToClient(client, "Could not verify room membership.", "MEMBERSHIP_CHECK_FAILED")
		return
	}
	if !isMemberDB {
		log.Printf("MH: Client %s is not a DB member of room %s. CHAT message rejected.", client.ID, socketMsg.ChatRoomID)
		mh.sendErrorToClient(client, "You are not a member of this chat room.", "NOT_A_MEMBER")
		return
	}

	// 2. Parse payload gửi từ client
	payload, err := ParsePayload[ChatMessageSendPayload](socketMsg.Data)
	if err != nil {
		log.Printf("MH: Error parsing CHAT message payload from client %s: %v", client.ID, err)
		mh.sendErrorToClient(client, "Invalid CHAT payload format", "INVALID_CHAT_PAYLOAD")
		return
	}
	if payload.Content == "" {
		mh.sendErrorToClient(client, "Message content cannot be empty", "EMPTY_CONTENT")
		return
	}

	if CheckContext(ctx, client.ID, "MH: Context canceled during CHAT message processing (after parse)") {
		return
	}

	// 3. Tạo đối tượng Message domain để lưu vào DB
	dbMessage := &domain.Message{
		ID:         uuid.New().String(), // Server tạo ID cho message
		SenderId:   socketMsg.SenderID,
		ChatRoomId: socketMsg.ChatRoomID,
		Type:       domain.TextMessageType, // Giả định là TEXT, có thể mở rộng dựa vào MimeType
		MimeType:   payload.MimeType,
		Content:    payload.Content,
		CreatedAt:  time.UnixMilli(socketMsg.Timestamp).UTC(), // Dùng timestamp từ server
	}

	// Logic xác định MessageType dựa trên MimeType
	if payload.MimeType != "" {
		if strings.HasPrefix(payload.MimeType, "image/") {
			dbMessage.Type = domain.ImageMessageType
		} else if strings.HasPrefix(payload.MimeType, "video/") {
			dbMessage.Type = domain.VideoMessageType
		} else if strings.HasPrefix(payload.MimeType, "audio/") {
			dbMessage.Type = domain.AudioMessageType
		} else if payload.MimeType != "text/plain" {
			dbMessage.Type = domain.FileMessageType
		}
	}

	// 4. Lưu message vào DB
	err = mh.messageRepository.CreateMessage(ctx, dbMessage)
	if err != nil {
		log.Printf("MH: Error saving message to DB from client %s: %v", client.ID, err)
		mh.sendErrorToClient(client, "Could not save your message.", "DB_SAVE_FAILED")
		return
	}

	// 5. Chuẩn bị message để broadcast (có thể enrich data)
	senderAccount, _ := mh.accountRepository.FindById(ctx, dbMessage.SenderId)
	senderName := "Unknown User"
	avatarURL := ""
	if senderAccount != nil {
		senderName = senderAccount.Name
		avatarURL = senderAccount.AvatarURL
	}

	receivePayload := ChatMessageReceivePayload{
		MessageID:  dbMessage.ID,
		SenderName: senderName,
		AvatarURL:  avatarURL,
		Content:    dbMessage.Content,
		MimeType:   dbMessage.MimeType,
	}

	// Tạo SocketMessage để gửi đi, với Type là NEW_MESSAGE
	broadcastMsg := SocketMessage{
		Type:       SocketMessageTypeNewMessage, // Server gửi xuống với type này
		ChatRoomID: dbMessage.ChatRoomId,
		SenderID:   dbMessage.SenderId, // Giữ nguyên sender gốc
		Timestamp:  dbMessage.CreatedAt.UnixMilli(),
		Data:       mustMarshal(receivePayload),
	}

	// 6. Gửi message đến tất cả thành viên online của phòng
	mh.hub.DeliverMessageToRoomRecipients(ctx, dbMessage.ChatRoomId, broadcastMsg)
	log.Printf("MH: CHAT message from %s for room %s processed and delivered.", client.ID, dbMessage.ChatRoomId)
}

func (mh *MessageHandler) handleJoinRoomMessage(client *Client, socketMsg SocketMessage, ctx context.Context) {
	if socketMsg.ChatRoomID == "" {
		mh.sendErrorToClient(client, "ChatRoomID is required for JOIN message", "JOIN_NO_ROOM_ID")
		return
	}

	// Kiểm tra client có phải là thành viên DB của phòng không
	isMemberDB, err := mh.chatRoomRepository.IsUserMemberOfChatRoom(ctx, client.ID, socketMsg.ChatRoomID)
	if err != nil {
		log.Printf("MH: Error checking DB membership for JOIN: client %s, room %s: %v", client.ID, socketMsg.ChatRoomID, err)
		mh.sendErrorToClient(client, "Could not verify room membership.", "JOIN_MEMBERSHIP_FAILED")
		return
	}
	if !isMemberDB {
		log.Printf("MH: Client %s is not a DB member of room %s. JOIN to active view rejected.", client.ID, socketMsg.ChatRoomID)
		mh.sendErrorToClient(client, "You are not a member of this room to join its active view.", "JOIN_NOT_MEMBER")
		return
	}

	mh.hub.JoinActiveRoomView(socketMsg.ChatRoomID, client)
	log.Printf("MH: JOIN message from client %s for room %s processed.", client.ID, socketMsg.ChatRoomID)
}

func (mh *MessageHandler) handleLeaveRoomMessage(client *Client, socketMsg SocketMessage, ctx context.Context) {
	if socketMsg.ChatRoomID == "" {
		mh.sendErrorToClient(client, "ChatRoomID is required for LEAVE message", "LEAVE_NO_ROOM_ID")
		return
	}
	mh.hub.LeaveActiveRoomView(socketMsg.ChatRoomID, client)
	log.Printf("MH: LEAVE message from client %s for room %s processed.", client.ID, socketMsg.ChatRoomID)
}

func (mh *MessageHandler) handleTypingMessage(client *Client, socketMsg SocketMessage, ctx context.Context) {
	if socketMsg.ChatRoomID == "" {
		// Không gửi lỗi, chỉ bỏ qua nếu roomID thiếu
		log.Printf("MH: TYPING message from client %s missing ChatRoomID.", client.ID)
		return
	}
	// Chỉ gửi cho những người đang active trong view
	if mh.hub.IsClientInActiveView(socketMsg.ChatRoomID, client.ID) {
		// Gửi message này tới các client khác trong active view, trừ sender
		mh.hub.broadcastToActiveView(socketMsg.ChatRoomID, socketMsg, client.ID)
	}
}

func (mh *MessageHandler) handleReadReceiptMessage(client *Client, socketMsg SocketMessage, ctx context.Context) {
	if socketMsg.ChatRoomID == "" {
		log.Printf("MH: READ_RECEIPT message from client %s missing ChatRoomID.", client.ID)
		return
	}
	payload, err := ParsePayload[ReadReceiptPayload](socketMsg.Data)
	if err != nil || payload.MessageID == "" {
		log.Printf("MH: Invalid READ_RECEIPT payload from client %s: %v", client.ID, err)
		// Không gửi lỗi cho client về việc này, chỉ log
		return
	}

	// Logic này phức tạp hơn:
	// 1. Xác nhận messageID tồn tại và thuộc về chatRoomID.
	// 2. Cập nhật trạng thái "đã đọc" trong DB (nếu có).
	// 3. Gửi thông báo "đã đọc" đến sender của message gốc (nếu sender đó online và active trong view).
	// Hoặc gửi cho tất cả mọi người trong active view biết "user X đã đọc đến message Y".

	// Hiện tại, đơn giản là broadcast cho active view:
	if mh.hub.IsClientInActiveView(socketMsg.ChatRoomID, client.ID) {
		mh.hub.broadcastToActiveView(socketMsg.ChatRoomID, socketMsg, client.ID)
	}
}

func (mh *MessageHandler) sendErrorToClient(client *Client, errorMsg string, errorCode string) {
	payload := ErrorPayload{Message: errorMsg, Code: errorCode}
	msg := SocketMessage{
		Type:      SocketMessageTypeError,
		SenderID:  "system",
		Timestamp: time.Now().UnixMilli(),
		Data:      mustMarshal(payload),
	}

	// Gửi không block
	select {
	case client.Send <- mustMarshal(msg):
	default:
		log.Printf("MH: Failed to send error message to client %s (channel full/closed). Error: %s", client.ID, errorMsg)
	}
}

func (mh *MessageHandler) sendPongToClient(client *Client) {
	msg := SocketMessage{
		Type:      SocketMessageTypePong,
		SenderID:  "system",
		Timestamp: time.Now().UTC().UnixMilli(),
	}

	select {
	case client.Send <- mustMarshal(msg):
	default:
		log.Printf("MH: Failed to send PONG to client %s (channel full/closed).", client.ID)
	}
}
