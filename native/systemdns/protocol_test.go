package systemdns

import "testing"

func TestSnapshots(t *testing.T) {
	for _, frame := range []string{
		`{"version":1,"servers":[]}`,
		`{"version":1,"servers":["192.0.2.1:53","[2001:db8::1]:53","[fe80::1%wlan0]:53"]}`,
	} {
		if _, err := decodeSnapshot([]byte(frame)); err != nil {
			t.Fatalf("valid snapshot: %v", err)
		}
	}
	for _, frame := range []string{
		`{"version":2,"servers":[]}`, `{"version":1}`, `{"version":1,"servers":null}`,
		`{"version":1,"servers":["example.com:53"]}`, `{"version":1,"servers":["192.0.2.1:5353"]}`,
		`{"version":1,"servers":["127.0.0.1:53"]}`, `{"version":1,"servers":["[::]:53"]}`,
		`{"version":1,"servers":[]} {}`, `{"version":1,"servers":[],"query":"example.com"}`,
	} {
		if _, err := decodeSnapshot([]byte(frame)); err == nil {
			t.Fatalf("accepted invalid snapshot: %s", frame)
		}
	}
}
