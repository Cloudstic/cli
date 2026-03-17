//go:build linux

package secretref

import (
	"context"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	secretServiceName      = "org.freedesktop.secrets"
	secretServicePath      = dbus.ObjectPath("/org/freedesktop/secrets")
	secretServiceInterface = "org.freedesktop.Secret.Service"
	collectionInterface    = "org.freedesktop.Secret.Collection"
	itemInterface          = "org.freedesktop.Secret.Item"
)

type secretServiceDBusConn interface {
	Object(dest string, path dbus.ObjectPath) dbus.BusObject
	Close() error
}

type secretServiceSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

var secretServiceSessionBus = func() (secretServiceDBusConn, error) {
	return dbus.SessionBus()
}

func defaultSecretServiceLookup(_ context.Context, collection, item string) (string, error) {
	conn, err := secretServiceSessionBus()
	if err != nil {
		return "", fmt.Errorf("%w: cannot connect to the session bus; ensure a desktop keyring/DBus session is available or use env://... as a fallback", errSecretServiceUnavailable)
	}
	defer conn.Close()

	service := conn.Object(secretServiceName, secretServicePath)
	collectionPath, err := lookupSecretServiceCollection(conn, service, collection)
	if err != nil {
		return "", err
	}
	itemPath, err := lookupSecretServiceItem(conn, collectionPath, item)
	if err != nil {
		return "", err
	}

	var ignored dbus.Variant
	var session dbus.ObjectPath
	if err := service.Call(secretServiceInterface+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&ignored, &session); err != nil {
		return "", mapSecretServiceCallError(err, "open Secret Service session")
	}

	var secret secretServiceSecret
	if err := conn.Object(secretServiceName, itemPath).Call(itemInterface+".GetSecret", 0, session).Store(&secret); err != nil {
		return "", mapSecretServiceCallError(err, "read secret from Secret Service")
	}
	return string(secret.Value), nil
}

func lookupSecretServiceCollection(conn secretServiceDBusConn, service dbus.BusObject, want string) (dbus.ObjectPath, error) {
	if aliasPath, err := readSecretServiceAlias(service, want); err == nil && aliasPath != "" && aliasPath != "/" {
		return aliasPath, nil
	}

	collectionsVar, err := service.GetProperty(secretServiceInterface + ".Collections")
	if err != nil {
		return "", mapSecretServiceCallError(err, "list Secret Service collections")
	}
	collections, ok := collectionsVar.Value().([]dbus.ObjectPath)
	if !ok {
		return "", fmt.Errorf("%w: unexpected Secret Service collections response", errSecretServiceUnavailable)
	}

	for _, path := range collections {
		label, err := readSecretServiceLabel(conn, path, collectionInterface)
		if err == nil && label == want {
			return path, nil
		}
		if lastSecretServicePathSegment(path) == want {
			return path, nil
		}
	}
	return "", errSecretServiceNotFound
}

func readSecretServiceAlias(service dbus.BusObject, alias string) (dbus.ObjectPath, error) {
	var path dbus.ObjectPath
	if err := service.Call(secretServiceInterface+".ReadAlias", 0, alias).Store(&path); err != nil {
		return "", mapSecretServiceCallError(err, "read Secret Service collection alias")
	}
	return path, nil
}

func lookupSecretServiceItem(conn secretServiceDBusConn, collectionPath dbus.ObjectPath, want string) (dbus.ObjectPath, error) {
	collection := conn.Object(secretServiceName, collectionPath)
	itemsVar, err := collection.GetProperty(collectionInterface + ".Items")
	if err != nil {
		return "", mapSecretServiceCallError(err, "list items in Secret Service collection")
	}
	items, ok := itemsVar.Value().([]dbus.ObjectPath)
	if !ok {
		return "", fmt.Errorf("%w: unexpected Secret Service items response", errSecretServiceUnavailable)
	}

	for _, path := range items {
		label, err := readSecretServiceLabel(conn, path, itemInterface)
		if err == nil && label == want {
			return path, nil
		}
		if lastSecretServicePathSegment(path) == want {
			return path, nil
		}
	}
	return "", errSecretServiceNotFound
}

func readSecretServiceLabel(conn secretServiceDBusConn, path dbus.ObjectPath, iface string) (string, error) {
	labelVar, err := conn.Object(secretServiceName, path).GetProperty(iface + ".Label")
	if err != nil {
		return "", mapSecretServiceCallError(err, "read Secret Service label")
	}
	label, ok := labelVar.Value().(string)
	if !ok {
		return "", fmt.Errorf("%w: unexpected Secret Service label response", errSecretServiceUnavailable)
	}
	return label, nil
}

func lastSecretServicePathSegment(path dbus.ObjectPath) string {
	p := strings.Trim(string(path), "/")
	if p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	return parts[len(parts)-1]
}

func mapSecretServiceCallError(err error, action string) error {
	if err == nil {
		return nil
	}
	switch e := err.(type) {
	case dbus.Error:
		return mapSecretServiceDBusErrorName(e.Name, action, err)
	case *dbus.Error:
		return mapSecretServiceDBusErrorName(e.Name, action, err)
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "dbus") || strings.Contains(lower, "secret service") {
		return fmt.Errorf("%w: %s failed (%v); use env://... as a fallback", errSecretServiceUnavailable, action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func mapSecretServiceDBusErrorName(name, action string, err error) error {
	switch name {
	case "org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.NoServer",
		"org.freedesktop.DBus.Error.Spawn.ExecFailed",
		"org.freedesktop.DBus.Error.Spawn.ServiceNotFound",
		"org.freedesktop.DBus.Error.FileNotFound",
		"org.freedesktop.DBus.Error.Disconnected":
		return fmt.Errorf("%w: %s failed because no Secret Service session/keyring daemon is available; use env://... as a fallback", errSecretServiceUnavailable, action)
	case "org.freedesktop.Secret.Error.NoSuchObject":
		return errSecretServiceNotFound
	default:
		return fmt.Errorf("%s: %w", action, err)
	}
}
