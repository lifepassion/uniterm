package credentials

import (
    "errors"
    "testing"
)

type unavailableKeychain struct{}
func (unavailableKeychain) Get(string) (string, error) { return "", errors.New("keychain locked") }
func (unavailableKeychain) Set(string, string) error { return errors.New("keychain locked") }
func (unavailableKeychain) Delete(string) error { return errors.New("keychain locked") }

func TestMasterPasswordWithoutKeychain(t *testing.T) {
    dir := t.TempDir()
    s := New(dir, unavailableKeychain{})
    if err := s.Setup(ModeMasterPassword, "test password"); err != nil { t.Fatal(err) }
    encrypted, err := s.Encrypt("中文连接密码")
    if err != nil { t.Fatal(err) }
    restarted := New(dir, unavailableKeychain{})
    if err := restarted.AutoUnlock(); err != nil { t.Fatal(err) }
    if restarted.Status().Unlocked || restarted.Status().NeedsSetup { t.Fatal("restart must request password, not setup") }
    if err := restarted.Unlock("test password"); err != nil { t.Fatal(err) }
    plain, err := restarted.Decrypt(encrypted)
    if err != nil || plain != "中文连接密码" { t.Fatalf("decrypt: %q, %v", plain, err) }
}

func TestKeychainModeStillRequiresDurableKey(t *testing.T) {
    dir := t.TempDir()
    s := New(dir, unavailableKeychain{})
    if err := s.Setup(ModeKeychain, ""); err == nil { t.Fatal("must not silently lose the only durable key") }
    meta, err := ReadMeta(dir)
    if err != nil || meta != nil || s.Status().Unlocked { t.Fatal("failed setup must not commit state") }
}

type lockedWrites struct{ *fakeKeychain }
func (lockedWrites) Set(string, string) error { return errors.New("writes locked") }

func TestPasswordChangeDoesNotReuseStaleCache(t *testing.T) {
    dir := t.TempDir()
    kc := newFakeKeychain()
    s := New(dir, kc)
    if err := s.Setup(ModeMasterPassword, "old password"); err != nil { t.Fatal(err) }
    // Simulate a password change while the old cache remains readable but cannot be updated.
    s.keychain = lockedWrites{kc}
    if err := s.Setup(ModeMasterPassword, "new password"); err != nil { t.Fatal(err) }
    encrypted, err := s.Encrypt("secret")
    if err != nil { t.Fatal(err) }
    restarted := New(dir, kc)
    if err := restarted.AutoUnlock(); err != nil { t.Fatal(err) }
    if restarted.Status().Unlocked { t.Fatal("stale cache must not unlock new metadata") }
    if err := restarted.Unlock("new password"); err != nil { t.Fatal(err) }
    if got, err := restarted.Decrypt(encrypted); err != nil || got != "secret" { t.Fatalf("decrypt: %q, %v", got, err) }
}
