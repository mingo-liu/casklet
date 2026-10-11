//go:build darwin

package registryauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Native Security/CoreFoundation calls keep release builds CGO_ENABLED=0 and
// avoid external credential helpers and passwords in process arguments.
type keychainAPI struct {
	stringCreate     func(uintptr, string, uint32) uintptr
	dataCreate       func(uintptr, []byte, int64) uintptr
	dataLength       func(uintptr) int64
	dataBytes        func(uintptr) unsafe.Pointer
	dictionaryCreate func(uintptr, int64, uintptr, uintptr) uintptr
	dictionarySet    func(uintptr, uintptr, uintptr)
	release          func(uintptr)
	copyMatching     func(uintptr, *uintptr) int32
	add              func(uintptr, uintptr) int32
	update           func(uintptr, uintptr) int32
	delete           func(uintptr) int32
	constants        map[string]uintptr
}

var loadKeychain = sync.OnceValues(func() (*keychainAPI, error) {
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, errors.New("load macOS CoreFoundation failed")
	}
	security, err := purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, errors.New("load macOS Security framework failed")
	}
	a := &keychainAPI{constants: map[string]uintptr{}}
	for _, binding := range []struct {
		target  any
		library uintptr
		name    string
	}{
		{&a.stringCreate, cf, "CFStringCreateWithCString"}, {&a.dataCreate, cf, "CFDataCreate"},
		{&a.dataLength, cf, "CFDataGetLength"}, {&a.dataBytes, cf, "CFDataGetBytePtr"},
		{&a.dictionaryCreate, cf, "CFDictionaryCreateMutable"}, {&a.dictionarySet, cf, "CFDictionarySetValue"}, {&a.release, cf, "CFRelease"},
		{&a.copyMatching, security, "SecItemCopyMatching"}, {&a.add, security, "SecItemAdd"}, {&a.update, security, "SecItemUpdate"}, {&a.delete, security, "SecItemDelete"},
	} {
		symbol, err := purego.Dlsym(binding.library, binding.name)
		if err != nil {
			return nil, fmt.Errorf("load Keychain API %s failed", binding.name)
		}
		purego.RegisterFunc(binding.target, symbol)
	}
	for _, symbol := range []string{"kSecClass", "kSecClassGenericPassword", "kSecAttrService", "kSecAttrAccount", "kSecValueData", "kSecReturnData", "kSecMatchLimit", "kSecMatchLimitOne", "kSecUseAuthenticationUI", "kSecUseAuthenticationUIFail"} {
		address, err := purego.Dlsym(security, symbol)
		if err != nil {
			return nil, fmt.Errorf("load Keychain constant %s failed", symbol)
		}
		a.constants[symbol] = cfConstant(address)
	}
	address, err := purego.Dlsym(cf, "kCFBooleanTrue")
	if err != nil {
		return nil, errors.New("load Keychain boolean failed")
	}
	a.constants["kCFBooleanTrue"] = cfConstant(address)
	return a, nil
})

// dlsym returns a native data-symbol address as an ABI word. Reinterpret that
// word as a C pointer and read the exported CFTypeRef; no Go heap pointer is
// converted into an integer or retained by the framework.
func cfConstant(address uintptr) uintptr {
	pointer := *(*unsafe.Pointer)(unsafe.Pointer(&address))
	return *(*uintptr)(pointer)
}

type keychainStore struct{ api *keychainAPI }

func OpenStore() (Store, error) {
	api, err := loadKeychain()
	if err != nil {
		return nil, err
	}
	return &keychainStore{api: api}, nil
}

func (s *keychainStore) query(registry string) (uintptr, func(), error) {
	if canonical, err := NormalizeRegistry(registry); err != nil || canonical != registry {
		return 0, nil, errors.New("invalid canonical registry")
	}
	a := s.api
	dictionary := a.dictionaryCreate(0, 0, 0, 0)
	service := a.stringCreate(0, "com.mingo-liu.casklet.registry", 0x08000100)
	account := a.stringCreate(0, registry, 0x08000100)
	cleanup := func() { a.release(dictionary); a.release(service); a.release(account) }
	a.dictionarySet(dictionary, a.constants["kSecClass"], a.constants["kSecClassGenericPassword"])
	a.dictionarySet(dictionary, a.constants["kSecAttrService"], service)
	a.dictionarySet(dictionary, a.constants["kSecAttrAccount"], account)
	// Never open a UI prompt during headless/CI pulls. Locked or denied entries
	// are explicit errors; only missing entries fall back to anonymous access.
	a.dictionarySet(dictionary, a.constants["kSecUseAuthenticationUI"], a.constants["kSecUseAuthenticationUIFail"])
	return dictionary, cleanup, nil
}

func keychainStatus(operation string, status int32) error {
	if status == 0 {
		return nil
	}
	if status == -25300 {
		return ErrNotFound
	}
	return fmt.Errorf("macOS Keychain %s failed (status %d); unlock your login Keychain and allow casklet access", operation, status)
}

func (s *keychainStore) Get(registry string) (Credential, error) {
	query, cleanup, err := s.query(registry)
	if err != nil {
		return Credential{}, err
	}
	defer cleanup()
	a := s.api
	a.dictionarySet(query, a.constants["kSecReturnData"], a.constants["kCFBooleanTrue"])
	a.dictionarySet(query, a.constants["kSecMatchLimit"], a.constants["kSecMatchLimitOne"])
	var result uintptr
	if err := keychainStatus("read", a.copyMatching(query, &result)); err != nil {
		return Credential{}, err
	}
	defer a.release(result)
	size := a.dataLength(result)
	if size < 1 || size > MaxEnvelope {
		return Credential{}, errors.New("invalid registry Keychain entry")
	}
	data := unsafe.Slice((*byte)(a.dataBytes(result)), int(size))
	var credential Credential
	if json.Unmarshal(data, &credential) != nil || credential.Validate(registry) != nil {
		return Credential{}, errors.New("invalid registry Keychain entry")
	}
	return credential, nil
}

func (s *keychainStore) Put(credential Credential) error {
	if err := credential.Validate(credential.Registry); err != nil || credential.Username == "" {
		return errors.New("invalid registry credentials")
	}
	query, cleanup, err := s.query(credential.Registry)
	if err != nil {
		return err
	}
	defer cleanup()
	a := s.api
	encoded, err := json.Marshal(credential)
	if err != nil {
		return errors.New("encode registry credentials failed")
	}
	defer clear(encoded)
	data := a.dataCreate(0, encoded, int64(len(encoded)))
	defer a.release(data)
	changes := a.dictionaryCreate(0, 0, 0, 0)
	defer a.release(changes)
	a.dictionarySet(changes, a.constants["kSecValueData"], data)
	status := a.update(query, changes)
	if status == -25300 {
		a.dictionarySet(query, a.constants["kSecValueData"], data)
		status = a.add(query, 0)
	}
	runtime.KeepAlive(encoded)
	return keychainStatus("write", status)
}

func (s *keychainStore) Delete(registry string) error {
	query, cleanup, err := s.query(registry)
	if err != nil {
		return err
	}
	defer cleanup()
	err = keychainStatus("delete", s.api.delete(query))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
