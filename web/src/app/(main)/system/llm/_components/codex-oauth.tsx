"use client";

import * as React from "react";

import { useTranslations } from "next-intl";

import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { pollCodexDevice } from "@/lib/codex-oauth";
import type { CodexOAuthStart, CodexOAuthStatus } from "@/lib/types";

// Mounted only while the Codex drawer is open. No credentials enter the browser.
export function CodexOAuthPanel() {
  const t = useTranslations("llmPage.oauth");
  const [status, setStatus] = React.useState<CodexOAuthStatus | null>(null);
  const [device, setDevice] = React.useState<CodexOAuthStart | null>(null);
  const [phase, setPhase] = React.useState("checking");
  const [error, setError] = React.useState("");
  const generation = React.useRef(0);
  const stopPolling = React.useRef<(() => void) | null>(null);

  React.useEffect(() => {
    const current = ++generation.current;
    api
      .codexOAuthStatus()
      .then((result) => {
        if (current !== generation.current) return;
        setStatus(result);
        setPhase(result.connected ? "connected" : "disconnected");
      })
      .catch((e: Error) => {
        if (current !== generation.current) return;
        setError(e.message);
        setPhase("error");
      });
    return () => {
      ++generation.current;
      stopPolling.current?.();
    };
  }, []);

  async function start() {
    const current = ++generation.current;
    stopPolling.current?.();
    setDevice(null);
    setError("");
    setPhase("starting");
    try {
      const result = await api.startCodexOAuth();
      if (current !== generation.current) return;
      const deadline = Date.parse(result.expires_at);
      const url = new URL(result.verification_url);
      if (
        url.origin !== "https://auth.openai.com" ||
        !result.user_code ||
        !Number.isFinite(deadline) ||
        !Number.isFinite(result.poll_interval) ||
        result.poll_interval <= 0
      ) {
        throw new Error(t("invalidResponse"));
      }
      setStatus({ connected: false });
      setDevice(result);
      setPhase("pending");
      stopPolling.current = pollCodexDevice({
        deadline,
        interval: result.poll_interval * 1000,
        poll: api.pollCodexOAuth,
        onState: (next) => {
          if (current !== generation.current) return;
          setDevice(null);
          setPhase(next);
          if (next === "connected") {
            api
              .codexOAuthStatus()
              .then((fresh) => {
                if (current !== generation.current) return;
                setStatus(fresh);
                setPhase(fresh.connected ? "connected" : "disconnected");
              })
              .catch((e: Error) => {
                if (current !== generation.current) return;
                setError(e.message);
                setPhase("error");
              });
          }
        },
        onError: (e) => {
          if (current !== generation.current) return;
          setDevice(null);
          setError(e.message);
          setPhase("error");
        },
      });
    } catch (e) {
      if (current !== generation.current) return;
      setError((e as Error).message);
      setPhase("error");
    }
  }

  async function logout() {
    const current = ++generation.current;
    stopPolling.current?.();
    setDevice(null);
    setStatus(null);
    setError("");
    setPhase("starting");
    try {
      await api.logoutCodexOAuth();
      const fresh = await api.codexOAuthStatus();
      if (current !== generation.current) return;
      setStatus(fresh);
      setPhase(fresh.connected ? "connected" : "disconnected");
    } catch (e) {
      if (current !== generation.current) return;
      setError((e as Error).message);
      setPhase("error");
    }
  }

  const busy = phase === "starting" || phase === "checking";
  return (
    <section className="grid gap-3 rounded-lg border p-3" aria-label={t("title")}>
      <p className="font-medium text-sm">{t("title")}</p>
      <p className="text-muted-foreground text-xs">{t("shared")}</p>
      <p role="status" className="text-sm">
        {t(phase)}
        {status?.connected && status.email ? ` · ${status.email}` : ""}
      </p>
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
      {device && (
        <div className="grid gap-2 text-sm">
          <a
            href={device.verification_url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-primary underline"
          >
            {t("openVerification")} · {device.verification_url}
          </a>
          <p>
            {t("code")} <code className="select-all font-mono font-semibold">{device.user_code}</code>
          </p>
          <Button
            type="button"
            variant="outline"
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(device.user_code);
              } catch (e) {
                setError((e as Error).message);
              }
            }}
          >
            {t("copy")}
          </Button>
          <p className="text-muted-foreground text-xs">
            {t("deadline", {
              expires: device.expires_at,
              seconds: device.poll_interval,
            })}
          </p>
        </div>
      )}
      <div className="flex gap-2">
        {!status?.connected && (
          <Button type="button" disabled={busy || phase === "pending"} onClick={start}>
            {t(phase === "expired" || phase === "cancelled" || phase === "error" ? "restart" : "login")}
          </Button>
        )}
        {(status?.connected || phase === "pending") && (
          <Button type="button" variant="outline" disabled={busy} onClick={logout}>
            {t(phase === "pending" ? "cancel" : "logout")}
          </Button>
        )}
      </div>
    </section>
  );
}
