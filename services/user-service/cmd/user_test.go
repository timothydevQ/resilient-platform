package main

import (
	"testing"
)

func newTestUserSvc() *UserService { return NewUserService("region-a") }

func validUserReq() CreateUserRequest {
	return CreateUserRequest{Email: "test@example.com", FirstName: "Tim", LastName: "Cote"}
}

func TestCreateUser_Success(t *testing.T) {
	svc := newTestUserSvc()
	u, err := svc.CreateUser(validUserReq())
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if u.ID == "" { t.Error("expected non-empty ID") }
	if !u.Active { t.Error("expected user to be active") }
}

func TestCreateUser_MissingEmail(t *testing.T) {
	svc := newTestUserSvc()
	req := validUserReq()
	req.Email = ""
	_, err := svc.CreateUser(req)
	if err == nil { t.Error("expected error for missing email") }
}

func TestCreateUser_InvalidEmail(t *testing.T) {
	svc := newTestUserSvc()
	req := validUserReq()
	req.Email = "not-an-email"
	_, err := svc.CreateUser(req)
	if err == nil { t.Error("expected error for invalid email") }
}

func TestCreateUser_DuplicateEmail(t *testing.T) {
	svc := newTestUserSvc()
	svc.CreateUser(validUserReq())
	_, err := svc.CreateUser(validUserReq())
	if err == nil { t.Error("expected error for duplicate email") }
}

func TestCreateUser_MissingFirstName(t *testing.T) {
	svc := newTestUserSvc()
	req := validUserReq()
	req.FirstName = ""
	_, err := svc.CreateUser(req)
	if err == nil { t.Error("expected error for missing first_name") }
}

func TestGetUser_Found(t *testing.T) {
	svc := newTestUserSvc()
	u, _ := svc.CreateUser(validUserReq())
	found, err := svc.GetUser(u.ID)
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if found.ID != u.ID { t.Error("wrong user returned") }
}

func TestGetUser_NotFound(t *testing.T) {
	svc := newTestUserSvc()
	_, err := svc.GetUser("nonexistent")
	if err == nil { t.Error("expected error for missing user") }
}

func TestUpdateUser_Success(t *testing.T) {
	svc := newTestUserSvc()
	u, _ := svc.CreateUser(validUserReq())
	updated, err := svc.UpdateUser(u.ID, CreateUserRequest{FirstName: "NewName"})
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if updated.FirstName != "NewName" { t.Errorf("expected NewName, got %s", updated.FirstName) }
}

func TestUpdateUser_NotFound(t *testing.T) {
	svc := newTestUserSvc()
	_, err := svc.UpdateUser("nonexistent", CreateUserRequest{FirstName: "Name"})
	if err == nil { t.Error("expected error for nonexistent user") }
}

func TestDeactivateUser_Success(t *testing.T) {
	svc := newTestUserSvc()
	u, _ := svc.CreateUser(validUserReq())
	err := svc.DeactivateUser(u.ID)
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	found, _ := svc.GetUser(u.ID)
	if found.Active { t.Error("expected user to be inactive after deactivation") }
}

func TestDeactivateUser_NotFound(t *testing.T) {
	svc := newTestUserSvc()
	err := svc.DeactivateUser("nonexistent")
	if err == nil { t.Error("expected error for nonexistent user") }
}

func TestUserStore_GetByEmail(t *testing.T) {
	svc := newTestUserSvc()
	u, _ := svc.CreateUser(validUserReq())
	found, ok := svc.store.GetByEmail(u.Email)
	if !ok { t.Fatal("expected to find user by email") }
	if found.ID != u.ID { t.Error("wrong user by email") }
}

func TestUserStore_GetByEmail_NotFound(t *testing.T) {
	store := NewUserStore()
	_, ok := store.GetByEmail("nobody@example.com")
	if ok { t.Error("expected not found") }
}

func TestStats_Region(t *testing.T) {
	svc := NewUserService("region-b")
	stats := svc.Stats()
	if stats["region"].(string) != "region-b" {
		t.Errorf("expected region-b, got %v", stats["region"])
	}
}

func TestStats_Count(t *testing.T) {
	svc := newTestUserSvc()
	svc.CreateUser(validUserReq())
	req2 := validUserReq()
	req2.Email = "other@example.com"
	svc.CreateUser(req2)
	stats := svc.Stats()
	if stats["total_users"].(int) != 2 {
		t.Errorf("expected 2 users, got %v", stats["total_users"])
	}
}
// create
// missing email
// invalid email
// duplicate
// get found
