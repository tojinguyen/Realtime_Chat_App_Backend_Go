package auth

import (
	"context"
	"errors"
	"fmt"
	domain "gochat-backend/internal/domain/auth"
	"gochat-backend/pkg/verification"
	"log"
	"mime/multipart"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type RegisterInput struct {
	Name     string                `json:"name" binding:"required,max=255"`
	Email    string                `json:"email" binding:"required,email,customEmail,max=255"`
	Password string                `json:"password" binding:"required,customPassword,min=6,max=255"`
	Avatar   *multipart.FileHeader `json:"avatar" binding:"required,omitempty"`
}

type VerifyRegistrationInput struct {
	Email string `json:"email" binding:"required,email,customEmail,max=255"`
	Code  string `json:"code" binding:"required"`
}

type RegisterOutput struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

func (a *authUseCase) Register(ctx context.Context, input RegisterInput) (*RegisterOutput, error) {
	log.Printf("Registering user with email: %s\n", input.Email)

	exists, err := a.accountRepository.ExistsByEmail(ctx, input.Email)

	if err != nil {
		log.Printf("Error checking if email exists: %v\n", err)
		return nil, fmt.Errorf("failed to check if email exists: %w", err)
	}

	if exists {
		log.Printf("Email already exists: %s\n", input.Email)
		return nil, errors.New("email already exists")
	}

	// Check if email exists in verification records and delete if found
	existingVerification, err := a.verificationRegisterRepository.GetVerificationCodeByEmail(ctx, input.Email)
	if err != nil {
		log.Printf("Error checking if email exists in verification records: %v\n", err)
		return nil, fmt.Errorf("failed to check verification records: %w", err)
	}

	if existingVerification != nil {
		// Delete the existing verification record
		err = a.verificationRegisterRepository.DeleteVerificationCode(ctx, existingVerification.ID)
		if err != nil {
			log.Printf("Error deleting existing verification record: %v\n", err)
			return nil, fmt.Errorf("failed to delete existing verification record: %w", err)
		}
		log.Printf("Deleted existing verification record for email: %s\n", input.Email)
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)

	if err != nil {
		log.Printf("Error hashing password: %v\n", err)
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	var avatarURL string

	if input.Avatar != nil {
		avatarURL, err = a.cloudstorage.UploadAvatar(input.Avatar, "avatars/temp")
		if err != nil {
			log.Printf("Error uploading avatar: %v\n", err)
			return nil, fmt.Errorf("failed to upload avatar: %w", err)
		}
		log.Printf("Avatar uploaded successfully. URL: %s\n", avatarURL)
	} else {
		avatarURL = a.cfg.DefaultAvatarURL
		log.Printf("Avatar is nil, using default avatar URL. Default URL: %s\n", a.cfg.DefaultAvatarURL)
	}

	userID := uuid.New().String()
	verificationCode := a.verificationService.GenerateCode()

	expiresAt := time.Now().UTC().Add(time.Duration(a.cfg.VerificationCodeExpireMinutes) * time.Minute)

	verificationRecord := &domain.RegistrationVerificationCode{
		ID:             uuid.New().String(),
		Email:          input.Email,
		Name:           input.Name,
		HashedPassword: string(hashedPassword),
		Avatar:         avatarURL,
		Code:           verificationCode,
		Type:           verification.VerificationCodeTypeRegister,
		ExpiresAt:      expiresAt,
		CreatedAt:      time.Now(),
	}

	err = a.verificationRegisterRepository.CreateVerificationCode(ctx, verificationRecord)
	if err != nil {
		log.Printf("Error saving verification data: %v\n", err)
		return nil, fmt.Errorf("failed to save verification data: %w", err)
	}

	if err := a.emailService.SendVerificationCode(input.Email, verificationCode, verification.VerificationCodeTypeRegister); err != nil {
		log.Printf("Error sending verification code: %v\n", err)
		return nil, fmt.Errorf("failed to send verification code: %w", err)
	}

	return &RegisterOutput{
		ID:        userID,
		Name:      input.Name,
		Email:     input.Email,
		AvatarURL: avatarURL,
	}, nil
}

func (a *authUseCase) VerifyRegistration(ctx context.Context, input VerifyRegistrationInput) (*RegisterOutput, error) {
	verificationRecord, err := a.verificationRegisterRepository.GetVerificationCodeByEmail(ctx, input.Email)
	if err != nil {
		return nil, fmt.Errorf("failed to get verification record: %w", err)
	}

	if verificationRecord == nil {
		return nil, errors.New("verification record not found")
	}

	// Check if code is expired
	if time.Now().UTC().After(verificationRecord.ExpiresAt) {
		return nil, errors.New("verification code has expired")
	}

	// Verify the code
	if verificationRecord.Code != input.Code {
		return nil, errors.New("invalid verification code")
	}

	// Create user account
	account := &domain.Account{
		Id:        uuid.NewString(),
		Name:      verificationRecord.Name,
		Email:     verificationRecord.Email,
		Password:  verificationRecord.HashedPassword,
		AvatarURL: verificationRecord.Avatar,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	// Save user to database
	err = a.accountRepository.CreateUser(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("failed to create user account: %w", err)
	}

	err = a.verificationRegisterRepository.DeleteVerificationCode(ctx, verificationRecord.ID)
	if err != nil {
		fmt.Printf("Failed to delete verification record: %v\n", err)
	}

	// If avatar was uploaded to temporary location, move it to permanent location
	if verificationRecord.Avatar != "" && strings.Contains(verificationRecord.Avatar, "avatars/temp") {
		newAvatarURL, err := a.cloudstorage.MoveAvatar(verificationRecord.Avatar, fmt.Sprintf("avatars/%s", account.Id))
		if err != nil {
			fmt.Printf("Failed to move avatar to permanent location: %v\n", err)
		} else {
			// Update the account with the new avatar URL
			account.AvatarURL = newAvatarURL
			err = a.accountRepository.UpdateAvatar(ctx, account.Id, newAvatarURL)
			if err != nil {
				fmt.Printf("Failed to update avatar URL: %v\n", err)
			}
		}
	}

	return &RegisterOutput{
		ID:        account.Id,
		Name:      account.Name,
		Email:     account.Email,
		AvatarURL: account.AvatarURL,
	}, nil
}
