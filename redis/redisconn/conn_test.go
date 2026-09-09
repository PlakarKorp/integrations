package redisconn

import "testing"

func TestParseConnConfigFromURI(t *testing.T) {
	cfg, err := ParseConnConfig(map[string]string{"location": "rediss://default:s3cr3t@redis.example.com:6380/2"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "redis.example.com" || cfg.Port != "6380" || cfg.Username != "default" || cfg.Password != "s3cr3t" || cfg.Database != "2" || !cfg.TLS {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestParseConnConfigOverridesURI(t *testing.T) {
	cfg, err := ParseConnConfig(map[string]string{
		"location":      "redis://:old@redis.example.com:6379/0",
		"host":          "127.0.0.1",
		"port":          "6381",
		"password":      "new",
		"database":      "4",
		"redis_bin_dir": "/opt/redis/bin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != "6381" || cfg.Password != "new" || cfg.Database != "4" || cfg.Bin() != "/opt/redis/bin/redis-cli" {
		t.Fatalf("overrides were not applied: %+v", cfg)
	}
}

func TestParseConnConfigRejectsBadScheme(t *testing.T) {
	if _, err := ParseConnConfig(map[string]string{"location": "http://redis.example.com"}); err == nil {
		t.Fatal("expected unsupported scheme error")
	}
}
