"use client";

import * as React from "react";

import { ChevronDownIcon, Loader2Icon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { WebShellDetail, WebShellInput, WebShellLang, WebShellMode, WebShellType } from "@/lib/types";
import { cn } from "@/lib/utils";

import { headersToText, parseHeaders, WEBSHELL_TYPES } from "./shared";

type FormState = {
  name: string;
  url: string;
  type: WebShellType;
  lang: WebShellLang;
  mode: WebShellMode;
  password: string;
  method: "GET" | "POST";
  param: string;
  encoding: string;
  timeout_seconds: string;
  insecure: boolean;
  headers: string;
  body_template: string;
  response_regex: string;
  response_b64: boolean;
  target: string;
  notes: string;
};

function blankForm(): FormState {
  return {
    name: "",
    url: "",
    type: "simple",
    lang: "php",
    mode: "raw",
    password: "",
    method: "POST",
    param: "cmd",
    encoding: "auto",
    timeout_seconds: "15",
    insecure: true,
    headers: "",
    body_template: "",
    response_regex: "",
    response_b64: false,
    target: "",
    notes: "",
  };
}

function detailToForm(d: WebShellDetail): FormState {
  return {
    name: d.name,
    url: d.url,
    type: (d.type as WebShellType) || "simple",
    lang: (d.lang as WebShellLang) || "php",
    mode: (d.mode as WebShellMode) || "raw",
    password: "",
    method: d.method === "GET" ? "GET" : "POST",
    param: d.param,
    encoding: d.encoding || "auto",
    timeout_seconds: String(d.timeout_seconds || 15),
    insecure: d.insecure,
    headers: headersToText(d.headers),
    body_template: d.body_template,
    response_regex: d.response_regex,
    response_b64: d.response_b64,
    target: d.target,
    notes: d.notes,
  };
}

export function WebShellFormDialog({
  open,
  onOpenChange,
  editId,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  editId: number | null;
  onSaved: (shell: WebShellDetail) => void;
}) {
  const [form, setForm] = React.useState<FormState>(blankForm);
  const [detail, setDetail] = React.useState<WebShellDetail | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [busy, setBusy] = React.useState<"save" | "test" | null>(null);
  const [advanced, setAdvanced] = React.useState(false);

  const isEdit = editId !== null;

  React.useEffect(() => {
    if (!open) return;
    if (editId === null) {
      setForm(blankForm());
      setDetail(null);
      setLoading(false);
      setAdvanced(false);
      return;
    }
    setLoading(true);
    api
      .webShell(editId)
      .then((d) => {
        setDetail(d);
        setForm(detailToForm(d));
      })
      .catch((e) => toast.error(`读取 WebShell 失败：${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, [open, editId]);

  const set = <K extends keyof FormState>(key: K, value: FormState[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  const changeType = (type: WebShellType) => {
    setForm((f) => {
      if (type === "behinder" || type === "godzilla") {
        return { ...f, type, lang: "php", method: "POST", param: "pass" };
      }
      if (type === "custom") {
        return { ...f, type, method: f.method || "POST", param: f.param || "cmd" };
      }
      if (type === "simple" && (f.param === "pass" || !f.param)) {
        return { ...f, type, param: "cmd" };
      }
      return { ...f, type };
    });
  };

  const submit = async (verifyAfter: boolean) => {
    const url = form.url.trim();
    if (!/^https?:\/\//i.test(url)) {
      toast.error("URL 需以 http:// 或 https:// 开头");
      return;
    }
    if (!isEdit && (form.type === "behinder" || form.type === "godzilla") && !form.password.trim()) {
      toast.error("冰蝎/哥斯拉必须填写连接密码");
      return;
    }
    if (isEdit && (form.type === "behinder" || form.type === "godzilla") && !detail?.password_set && !form.password.trim()) {
      toast.error("冰蝎/哥斯拉必须填写连接密码");
      return;
    }
    let headers: Record<string, string>;
    try {
      headers = parseHeaders(form.headers);
    } catch (e) {
      toast.error((e as Error).message);
      return;
    }
    const timeout = Number(form.timeout_seconds);
    const payload: WebShellInput = {
      name: form.name.trim(),
      url,
      type: form.type,
      lang: form.lang,
      mode: form.mode,
      method: form.method,
      param: form.param.trim(),
      headers,
      body_template: form.body_template,
      response_regex: form.response_regex.trim(),
      response_b64: form.response_b64,
      encoding: form.encoding.trim() || "auto",
      insecure: form.insecure,
      timeout_seconds: Number.isFinite(timeout) && timeout > 0 ? timeout : 15,
      target: form.target.trim(),
      notes: form.notes.trim(),
    };
    if (form.password.trim()) payload.password = form.password;

    setBusy(verifyAfter ? "test" : "save");
    try {
      const saved = isEdit ? await api.updateWebShell(editId, payload) : await api.createWebShell(payload);
      if (verifyAfter) {
        try {
          const r = await api.testWebShell(saved.id);
          if (r.ok) {
            toast.success(`已保存，连接成功${r.os_type ? `（${r.os_type === "windows" ? "Windows" : "Linux"}）` : ""}`);
          } else {
            toast.error(`已保存，但连接测试失败：${r.error || "目标未执行测试命令"}`);
          }
        } catch (e) {
          toast.error(`已保存，但测试失败：${(e as Error).message}`);
        }
      } else {
        toast.success(isEdit ? "已保存" : "已添加");
      }
      onSaved(saved);
      onOpenChange(false);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const encrypted = form.type === "behinder" || form.type === "godzilla";

  return (
    <Dialog open={open} onOpenChange={(o) => !busy && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{isEdit ? "编辑 WebShell" : "添加 WebShell"}</DialogTitle>
          <DialogDescription>
            Agent 拿到目标 shell 后会自动登记；这里也可以手动添加并连接。密码只保存在后端，列表接口不会回传。
          </DialogDescription>
        </DialogHeader>

        {loading ? (
          <div className="text-muted-foreground flex items-center justify-center gap-2 py-10 text-sm">
            <Loader2Icon className="size-4 animate-spin" /> 加载中…
          </div>
        ) : (
          <div className="grid gap-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label htmlFor="ws-name">名称</Label>
                <Input
                  id="ws-name"
                  value={form.name}
                  onChange={(e) => set("name", e.target.value)}
                  placeholder="留空自动取主机 + 路径"
                />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="ws-type">类型</Label>
                <Select value={form.type} onValueChange={(v) => changeType(v as WebShellType)}>
                  <SelectTrigger id="ws-type" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {WEBSHELL_TYPES.map((t) => (
                      <SelectItem key={t.value} value={t.value}>
                        {t.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="grid gap-1.5">
              <Label htmlFor="ws-url">URL</Label>
              <Input
                id="ws-url"
                value={form.url}
                onChange={(e) => set("url", e.target.value)}
                placeholder="http://target/upload/shell.php"
                className="font-mono"
              />
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label htmlFor="ws-pass">
                  连接密码
                  {isEdit && detail?.password_set ? (
                    <span className="text-muted-foreground ml-1 text-xs font-normal">（留空保持不变）</span>
                  ) : null}
                </Label>
                <Input
                  id="ws-pass"
                  type="password"
                  autoComplete="new-password"
                  value={form.password}
                  onChange={(e) => set("password", e.target.value)}
                  placeholder={encrypted ? "必填" : "可选"}
                />
              </div>
              {encrypted ? (
                <div className="bg-muted/50 text-muted-foreground flex items-end rounded-lg px-3 py-2 text-xs">
                  冰蝎/哥斯拉固定使用 PHP + POST + pass 参数，传输变体在连接测试时自动探测。
                </div>
              ) : (
                <div className="grid gap-1.5">
                  <Label htmlFor="ws-lang">脚本语言</Label>
                  <Select value={form.lang} onValueChange={(v) => set("lang", v as WebShellLang)}>
                    <SelectTrigger id="ws-lang" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="php">PHP</SelectItem>
                      <SelectItem value="jsp">JSP</SelectItem>
                      <SelectItem value="asp">ASP</SelectItem>
                      <SelectItem value="aspx">ASPX</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              )}
            </div>

            {form.type === "simple" && (
              <div className="grid gap-3 sm:grid-cols-3">
                <div className="grid gap-1.5">
                  <Label htmlFor="ws-mode">执行方式</Label>
                  <Select value={form.mode} onValueChange={(v) => set("mode", v as WebShellMode)}>
                    <SelectTrigger id="ws-mode" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="raw">raw（参数即命令）</SelectItem>
                      <SelectItem value="php_eval">php_eval（PHP 代码）</SelectItem>
                      <SelectItem value="jsp_eval">jsp_eval（Java 代码）</SelectItem>
                      <SelectItem value="asp_eval">asp_eval（VBScript）</SelectItem>
                      <SelectItem value="aspx_eval">aspx_eval（C# 代码）</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="ws-method">请求方法</Label>
                  <Select value={form.method} onValueChange={(v) => set("method", v as "GET" | "POST")}>
                    <SelectTrigger id="ws-method" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="POST">POST</SelectItem>
                      <SelectItem value="GET">GET</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="ws-param">参数名</Label>
                  <Input
                    id="ws-param"
                    value={form.param}
                    onChange={(e) => set("param", e.target.value)}
                    placeholder="cmd"
                    className="font-mono"
                  />
                </div>
              </div>
            )}

            {form.type === "custom" && (
              <>
                <div className="grid gap-3 sm:grid-cols-3">
                  <div className="grid gap-1.5">
                    <Label htmlFor="ws-method-c">请求方法</Label>
                    <Select value={form.method} onValueChange={(v) => set("method", v as "GET" | "POST")}>
                      <SelectTrigger id="ws-method-c" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="POST">POST</SelectItem>
                        <SelectItem value="GET">GET</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="grid gap-1.5 sm:col-span-2">
                    <Label htmlFor="ws-param-c">参数名</Label>
                    <Input
                      id="ws-param-c"
                      value={form.param}
                      onChange={(e) => set("param", e.target.value)}
                      placeholder="cmd"
                      className="font-mono"
                    />
                  </div>
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="ws-tpl">请求体模板</Label>
                  <Textarea
                    id="ws-tpl"
                    value={form.body_template}
                    onChange={(e) => set("body_template", e.target.value)}
                    rows={3}
                    className="font-mono text-xs"
                    placeholder={"pass={{pass}}&cmd={{cmd}}\n或 JSON：{\"pass\":\"{{pass}}\",\"cmd\":\"{{cmd_b64}}\"}"}
                  />
                  <p className="text-muted-foreground text-xs">
                    支持占位符：{"{{cmd}}"}、{"{{cmd_b64}}"}、{"{{cmd_url}}"}、{"{{pass}}"}、{"{{param}}"}。
                  </p>
                </div>
              </>
            )}

            <button
              type="button"
              onClick={() => setAdvanced((v) => !v)}
              className="text-muted-foreground hover:text-foreground flex w-fit items-center gap-1 text-xs"
            >
              <ChevronDownIcon className={cn("size-3.5 transition-transform", advanced && "rotate-180")} />
              高级选项（请求头 / 响应提取 / 超时 / 证书）
            </button>

            {advanced && (
              <div className="grid gap-3">
                <div className="grid gap-1.5">
                  <Label htmlFor="ws-headers">额外请求头（每行 Key: Value）</Label>
                  <Textarea
                    id="ws-headers"
                    value={form.headers}
                    onChange={(e) => set("headers", e.target.value)}
                    rows={2}
                    className="font-mono text-xs"
                    placeholder={"Cookie: PHPSESSID=...\nX-Forwarded-For: 127.0.0.1"}
                  />
                </div>
                <div className="grid gap-3 sm:grid-cols-3">
                  <div className="grid gap-1.5">
                    <Label htmlFor="ws-encoding">传输编码</Label>
                    <Input
                      id="ws-encoding"
                      value={form.encoding}
                      onChange={(e) => set("encoding", e.target.value)}
                      className="font-mono"
                      placeholder="auto"
                    />
                  </div>
                  <div className="grid gap-1.5">
                    <Label htmlFor="ws-timeout">请求超时（秒）</Label>
                    <Input
                      id="ws-timeout"
                      type="number"
                      min={3}
                      max={120}
                      value={form.timeout_seconds}
                      onChange={(e) => set("timeout_seconds", e.target.value)}
                    />
                  </div>
                  <div className="grid gap-1.5">
                    <Label htmlFor="ws-target">目标标识</Label>
                    <Input
                      id="ws-target"
                      value={form.target}
                      onChange={(e) => set("target", e.target.value)}
                      placeholder="默认取 URL 主机"
                    />
                  </div>
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="grid gap-1.5">
                    <Label htmlFor="ws-regex">响应正则（取第 1 个分组）</Label>
                    <Input
                      id="ws-regex"
                      value={form.response_regex}
                      onChange={(e) => set("response_regex", e.target.value)}
                      className="font-mono"
                      placeholder="可选"
                    />
                  </div>
                  <div className="flex items-center justify-between gap-3 rounded-lg border px-3">
                    <div>
                      <Label htmlFor="ws-b64" className="text-sm">
                        响应体 base64
                      </Label>
                      <p className="text-muted-foreground text-xs">目标把输出整体 base64 编码时开启</p>
                    </div>
                    <Switch id="ws-b64" checked={form.response_b64} onCheckedChange={(v) => set("response_b64", v)} />
                  </div>
                </div>
                <div className="flex items-center justify-between gap-3 rounded-lg border px-3 py-2">
                  <div>
                    <Label htmlFor="ws-insecure" className="text-sm">
                      跳过 HTTPS 证书校验
                    </Label>
                    <p className="text-muted-foreground text-xs">自签名证书目标需要开启（默认开启）</p>
                  </div>
                  <Switch id="ws-insecure" checked={form.insecure} onCheckedChange={(v) => set("insecure", v)} />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="ws-notes">备注</Label>
                  <Textarea
                    id="ws-notes"
                    value={form.notes}
                    onChange={(e) => set("notes", e.target.value)}
                    rows={2}
                    placeholder="利用链 / 上传路径 / 注意事项"
                  />
                </div>
              </div>
            )}
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={!!busy}>
            取消
          </Button>
          <Button variant="secondary" onClick={() => submit(true)} disabled={!!busy || loading}>
            {busy === "test" && <Loader2Icon className="animate-spin" />}
            保存并测试
          </Button>
          <Button onClick={() => submit(false)} disabled={!!busy || loading}>
            {busy === "save" && <Loader2Icon className="animate-spin" />}
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
