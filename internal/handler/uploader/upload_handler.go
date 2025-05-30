package handler

import (
	"gochat-backend/internal/handler"
	"gochat-backend/internal/usecase/upload"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

type SignatureRequest struct {
	ResourceType string `json:"resourceType"`
}

// HandleUploadSignature godoc
// @Summary Get Cloudinary Upload Signature
// @Description Generates a signature and parameters for direct client-side upload to Cloudinary for chat files.
// @Tags Upload
// @Accept  json
// @Produce  json
// @Param request body SignatureRequest false "Optional parameters"
// @Security BearerAuth
// @Success 200 {object} handler.APIResponse{data=cloudinaryinfra.UploadSignatureResponse} "Successfully generated signature"
// @Failure 401 {object} handler.APIResponse "Unauthorized"
// @Failure 500 {object} handler.APIResponse "Internal server error"
// @Router /api/v1/uploads/file-signature [post]
func HandleUploadSignature(c *gin.Context, upload upload.UploaderUseCase) {
	_, exists := c.Get("userId")
	if !exists {
		handler.SendErrorResponse(c, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req SignatureRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req.ResourceType = "image"
	}

	folder := "chat_files"

	if req.ResourceType == "" {
		req.ResourceType = "image"
	}

	signatureResponse, err := upload.GenerateUploadSignature(folder, req.ResourceType)
	if err != nil {
		log.Printf("Error generating upload signature: %v", err)
		handler.SendErrorResponse(c, http.StatusInternalServerError, "Failed to generate upload signature")
		return
	}

	handler.SendSuccessResponse(c, http.StatusOK, "Signature generated successfully", signatureResponse)
}
