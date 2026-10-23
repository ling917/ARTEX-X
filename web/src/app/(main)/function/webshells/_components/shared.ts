import type { WebShell } from "@/lib/types";

export const WEBSHELL_TYPES = [
  { value: "simple", label: "一句话命令马" },
  { value: "behinder", label: "冰蝎 Behinder" },
  { value: "godzilla", label: "哥斯拉 Godzilla" },
  { value: "custom", label: "自定义请求模板" },
] as const;

export function typeLabel(type: string): string {
  return WEBSHELL_TYPES.find((t) => t.value === type)?.label ?? type ?? "—";
}

export function osLabel(os: string): string {
  if (os === "linux") return "Linux / Unix";
  if (os === "windows") return "Windows";
  return "未知";
}

export function statusLabel(status: string): string {
  if (status === "ok") return "连接正常";
  if (status === "fail") return "连接失败";
  return "未测试";
}

export function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

export function fmtTime(ts?: string | number): string {
  if (!ts) return "—";
  const d = typeof ts === "number" ? new Date(ts * 1000) : new Date(ts);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("zh-CN", {
    year: "2-digit",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function shellAddress(shell: WebShell): string {
  return shell.target || shell.url;
}

export function b64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export function bytesToB64(bytes: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...Array.from(bytes.subarray(i, i + chunk)));
  }
  return btoa(binary);
}

export function decodeTextBytes(bytes: Uint8Array): { text: string; binary: boolean } {
  if (bytes.includes(0)) return { text: "", binary: true };
  try {
    return { text: new TextDecoder("utf-8", { fatal: true }).decode(bytes), binary: false };
  } catch {
    return { text: "", binary: true };
  }
}

export function downloadBytes(bytes: Uint8Array, name: string) {
  const blob = new Blob([new Uint8Array(bytes)], { type: "application/octet-stream" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name || "download";
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

export function joinRemotePath(base: string, name: string, windows: boolean): string {
  if (!base) return name;
  const sep = windows ? "\\" : "/";
  if (base.endsWith("/") || base.endsWith("\\")) return base + name;
  return base + sep + name;
}

export function parseHeaders(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;
    const idx = line.indexOf(":");
    if (idx <= 0) throw new Error(`请求头格式错误：${line}（应为 Key: Value）`);
    out[line.slice(0, idx).trim()] = line.slice(idx + 1).trim();
  }
  return out;
}

export function headersToText(headers: Record<string, string> | null | undefined): string {
  if (!headers) return "";
  return Object.entries(headers)
    .map(([k, v]) => `${k}: ${v}`)
    .join("\n");
}
