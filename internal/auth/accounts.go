package auth

import (
	"fmt"
	"sort"
)

// AccountInfo summarizes one stored account for display.
type AccountInfo struct {
	Email   string
	Active  bool
	EnvID   string
	Expired bool
}

// ListAccounts returns all stored accounts, sorted by email.
func ListAccounts() ([]AccountInfo, error) {
	s, err := loadStore()
	if err != nil {
		return nil, err
	}
	out := make([]AccountInfo, 0, len(s.Accounts))
	for email, a := range s.Accounts {
		envID := ""
		if hasV2Environment(a) {
			envID = a.EnvID
		}
		out = append(out, AccountInfo{
			Email:   email,
			Active:  email == s.Active,
			EnvID:   envID,
			Expired: TokenExpired(a.Token),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out, nil
}

// ActiveEmail returns the email of the active account, or "" if none.
func ActiveEmail() string {
	s, err := loadStore()
	if err != nil {
		return ""
	}
	return s.Active
}

// SwitchAccount makes email the active account. Errors if no such account is
// stored (the caller should log in to it first).
func SwitchAccount(email string) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	s, err := loadStore()
	if err != nil {
		return err
	}
	if _, ok := s.Accounts[email]; !ok {
		return fmt.Errorf("no stored account %q — run 'agend login --email %s' first", email, email)
	}
	s.Active = email
	return saveStore(s)
}

// RemoveAccount deletes a stored account. If it was active, another remaining
// account (if any) becomes active.
func RemoveAccount(email string) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	s, err := loadStore()
	if err != nil {
		return err
	}
	if _, ok := s.Accounts[email]; !ok {
		return fmt.Errorf("no stored account %q", email)
	}
	delete(s.Accounts, email)
	if s.Active == email {
		s.Active = ""
		for k := range s.Accounts {
			s.Active = k
			break
		}
	}
	return saveStore(s)
}

// RemoveAllAccounts clears every stored account (logout --all).
func RemoveAllAccounts() error {
	storeMu.Lock()
	defer storeMu.Unlock()
	s := &store{Version: storeVersion, Accounts: map[string]*account{}}
	return saveStore(s)
}
