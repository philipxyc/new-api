// Copilot's token exchange and inference endpoints are not a supported public
// API. Use only with the account holder's entitlement, never to resell access.
const GITHUB = "https://github.com";
const GITHUB_API = "https://api.github.com";
const CLIENT_ID = "Iv1.b507a08c87ecfe98";
const USER_AGENT = "NewAPI-Copilot/1.0";
const API_ORIGINS = ["https://api.githubcopilot.com", "https://api.business.githubcopilot.com", "https://api.enterprise.githubcopilot.com"];

export const meta = {
  apiVersion: 1,
  key: "github-copilot",
  name: "GitHub Copilot",
  icon: "GithubCopilot",
  version: "1.2.0",
  author: { name: "QuantumNous" },
  description: {
    en: "GitHub Copilot chat with upstream device authorization",
    zh: "通过设备授权使用 GitHub Copilot 对话模型",
  },
  baseUrl: API_ORIGINS[0],
  allowedHosts: ["github.com", "api.github.com"],
  keyHint: {
    en: "Save without a key, then sign in to GitHub on the channel",
    zh: "留空密钥保存渠道，然后在渠道中登录 GitHub",
  },
  catalogRouting: true,
  relay: [
    { incoming_path: "/v1/chat/completions", upstream_path: "/chat/completions" },
    { incoming_path: "/v1/responses", upstream_path: "/responses" },
    { incoming_path: "/v1/messages", upstream_path: "/v1/messages" },
    { incoming_path: "/v1/models", upstream_path: "/models", converter: "plugin" },
  ],
};

function apiOrigin(value) {
  const origin = String(value || "").replace(/\/+$/, "");
  if (!API_ORIGINS.includes(origin)) {
    throw new Error("Use an official GitHub Copilot API origin as the channel Base URL");
  }
  return origin;
}

function githubRequest(path, token, body) {
  const headers = { Accept: "application/json", "User-Agent": USER_AGENT };
  if (token) headers.Authorization = "Bearer " + token;
  const request = { url: (body ? GITHUB : GITHUB_API) + path, headers };
  if (body) {
    request.method = "POST";
    headers["Content-Type"] = "application/json";
    request.body = JSON.stringify(body);
  }
  return utils.fetch(request);
}

function responseObject(response, operation) {
  if (response.status !== 200 || !response.body || typeof response.body !== "object" || Array.isArray(response.body)) {
    throw new Error(operation + " failed (HTTP " + response.status + ")");
  }
  return response.body;
}

function secret(value) {
  // oxlint-disable-next-line no-control-regex -- Credentials must be safe HTTP header values.
  return typeof value === "string" && value.length > 0 && value.length <= 8192 && !/[\s\x00-\x1f\x7f]/.test(value);
}

function seconds(value, maximum) {
  return Number.isInteger(value) && value > 0 && value <= maximum;
}

function credentialResult(ctx, github, account) {
  const response = githubRequest("/copilot_internal/v2/token", github.githubToken);
  if (response.status === 401 || response.status === 403) return { relogin: true };
  const issued = responseObject(response, "Copilot token exchange");
  const now = utils.unixNow();
  if (!secret(issued.token) || !Number.isInteger(issued.expires_at) || issued.expires_at <= now + 60) {
    throw new Error("Copilot token exchange returned an invalid token or expiry");
  }
  const origin = apiOrigin(ctx.baseUrl);
  const assignedOrigin = apiOrigin(issued.endpoints && issued.endpoints.api);
  if (assignedOrigin !== origin) {
    throw new Error("Set the channel Base URL to " + assignedOrigin + ", then retry this sign-in without restarting GitHub authorization");
  }
  let expiresAt = issued.expires_at;
  if (seconds(issued.refresh_in, 86400)) expiresAt = Math.min(expiresAt, now + issued.refresh_in);
  if (github.githubExpiresAt) expiresAt = Math.min(expiresAt, github.githubExpiresAt);
  return {
    credential: { ...github, copilotToken: issued.token, apiOrigin: origin },
    expiresAt,
    accountId: account.id,
    label: account.login,
  };
}

export function checkKey() {
  return { error: { en: "Use the channel's GitHub device sign-in instead of a static key", zh: "请使用渠道的 GitHub 设备登录，不要填写静态密钥" } };
}

export function login(ctx, input) {
  apiOrigin(ctx.baseUrl);
  if (input === null) {
    const body = responseObject(githubRequest("/login/device/code", "", { client_id: CLIENT_ID, scope: "read:user" }), "GitHub device authorization");
    const interval = body.interval === undefined ? 5 : body.interval;
    if (
      !secret(body.device_code) ||
      !secret(body.user_code) ||
      body.verification_uri !== GITHUB + "/login/device" ||
      !seconds(body.expires_in, 900) ||
      !seconds(interval, 900)
    ) {
      throw new Error("GitHub returned an invalid device authorization");
    }
    return {
      type: "device",
      userCode: body.user_code,
      verificationUri: body.verification_uri,
      interval,
      expiresIn: body.expires_in,
      session: { deviceCode: body.device_code, interval, expiresAt: utils.unixNow() + body.expires_in },
    };
  }
  const session = ctx.login.session;
  if (!input.poll || !session || utils.unixNow() >= session.expiresAt) {
    throw new Error("GitHub device authorization expired; start sign-in again");
  }
  // Keep the issued GitHub token in server-side flow state before making
  // further requests, so a failed exchange can retry without reusing the code.
  if (!session.github) {
    const response = githubRequest("/login/oauth/access_token", "", {
      client_id: CLIENT_ID,
      device_code: session.deviceCode,
      grant_type: "urn:ietf:params:oauth:grant-type:device_code",
    });
    const body = responseObject(response, "GitHub device polling");
    if (body.error === "authorization_pending") return { type: "pending", interval: session.interval };
    if (body.error === "slow_down") {
      const interval = Math.max(session.interval + 5, seconds(body.interval, 900) ? body.interval : 0);
      if (interval > 900) throw new Error("GitHub device polling exceeded its retry interval");
      return { type: "pending", interval, session: { ...session, interval } };
    }
    if (body.error || !secret(body.access_token) || body.token_type !== "bearer") {
      throw new Error("GitHub device authorization was denied or expired; start sign-in again");
    }
    const github = { githubToken: body.access_token };
    if (body.expires_in !== undefined) {
      if (!seconds(body.expires_in, 31536000) || !secret(body.refresh_token)) {
        throw new Error("GitHub returned an invalid expiring authorization");
      }
      github.githubExpiresAt = utils.unixNow() + body.expires_in;
      github.githubRefreshToken = body.refresh_token;
    }
    return { type: "pending", interval: 1, session: { expiresAt: session.expiresAt, github } };
  }
  const user = responseObject(githubRequest("/user", session.github.githubToken), "GitHub account lookup");
  if (!Number.isSafeInteger(user.id) || user.id <= 0 || typeof user.login !== "string" || !/^[a-zA-Z0-9_-]{1,100}$/.test(user.login)) {
    throw new Error("GitHub returned an invalid account identity");
  }
  const result = credentialResult(ctx, session.github, { id: String(user.id), login: user.login });
  if (result.relogin) throw new Error("This GitHub authorization has no usable Copilot entitlement");
  return { type: "done", ...result };
}

export function refresh(ctx) {
  const previous = ctx.credential;
  if (!previous || !secret(previous.githubToken)) return { relogin: true };
  let github = {
    githubToken: previous.githubToken,
    githubExpiresAt: previous.githubExpiresAt,
    githubRefreshToken: previous.githubRefreshToken,
  };
  if (github.githubExpiresAt && github.githubExpiresAt <= utils.unixNow() + 60) {
    if (!secret(github.githubRefreshToken)) return { relogin: true };
    const response = githubRequest("/login/oauth/access_token", "", {
      client_id: CLIENT_ID,
      grant_type: "refresh_token",
      refresh_token: github.githubRefreshToken,
    });
    if (response.status === 401) return { relogin: true };
    if (response.status === 400 && response.body && (response.body.error === "bad_refresh_token" || response.body.error === "invalid_grant"))
      return { relogin: true };
    const body = responseObject(response, "GitHub authorization refresh");
    if (body.error === "bad_refresh_token" || body.error === "invalid_grant") return { relogin: true };
    if (body.error || body.token_type !== "bearer" || !secret(body.access_token) || !secret(body.refresh_token) || !seconds(body.expires_in, 31536000)) {
      throw new Error("GitHub authorization refresh returned an invalid result");
    }
    github = {
      githubToken: body.access_token,
      githubRefreshToken: body.refresh_token,
      githubExpiresAt: utils.unixNow() + body.expires_in,
    };
  }
  return credentialResult(ctx, github, {});
}

export function signRequest(ctx, request) {
  const credential = ctx.credential;
  if (
    !credential ||
    !secret(credential.copilotToken) ||
    apiOrigin(ctx.baseUrl) !== credential.apiOrigin ||
    !request.url.startsWith(credential.apiOrigin + "/")
  ) {
    throw new Error("Copilot credentials may only be sent to their assigned official API origin");
  }
  return {
    headers: {
      Authorization: "Bearer " + credential.copilotToken,
      "User-Agent": USER_AGENT,
      "Copilot-Integration-Id": "vscode-chat",
      "Openai-Intent": "conversation-panel",
      "X-GitHub-Api-Version": "2025-04-01",
      "X-Initiator": "user",
      "X-Request-Id": utils.uuid(),
    },
  };
}

export function prepareRequest(_ctx, request) {
  const messages = request.body && (request.body.messages || request.body.input);
  if (
    Array.isArray(messages) &&
    messages.some(
      (message) =>
        message && Array.isArray(message.content) && message.content.some((part) => part && (part.type === "image_url" || part.type === "input_image"))
    )
  ) {
    return { effects: [{ type: "header.patch", set: { "Copilot-Vision-Request": "true" } }] };
  }
}

export function convertResponse(_ctx, response) {
  if (response.status !== 200) {
    return { error: { status: response.status, message: "Copilot model discovery failed" } };
  }
  const body = JSON.parse(response.body);
  if (!body || !Array.isArray(body.data)) throw new Error("Copilot returned an invalid model catalog");
  const data = [];
  for (const model of body.data) {
    if (!model || typeof model.id !== "string" || !model.id || (model.capabilities && model.capabilities.type && model.capabilities.type !== "chat")) continue;
    if (!Array.isArray(model.supported_endpoints)) continue;
    const chat = model.supported_endpoints.includes("/chat/completions");
    const responses = model.supported_endpoints.includes("/responses");
    const messages = model.supported_endpoints.includes("/v1/messages");
    const routes = [];
    if (chat || responses || messages) {
      routes.push({
        incoming_path: "/v1/chat/completions",
        upstream_path: chat ? "/chat/completions" : responses ? "/responses" : "/v1/messages",
        converter: chat ? "none" : responses ? "openai_chat_completions_to_openai_responses" : "openai_chat_completions_to_anthropic_messages",
      });
      routes.push({
        incoming_path: "/v1/messages",
        upstream_path: messages ? "/v1/messages" : responses ? "/responses" : "/chat/completions",
        converter: messages ? "none" : responses ? "claude_messages_to_openai_responses" : "anthropic_messages_to_openai_chat_completions",
      });
    }
    if (responses || chat) {
      routes.push({
        incoming_path: "/v1/responses",
        upstream_path: responses ? "/responses" : "/chat/completions",
        converter: responses ? "none" : "openai_responses_to_openai_chat_completions",
      });
    }
    if (routes.length) {
      data.push({
        id: model.id,
        name: model.name,
        object: "model",
        owned_by: "github-copilot",
        routes,
        responses_websocket: responses && model.supported_endpoints.includes("ws:/responses"),
      });
    }
  }
  return {
    body: { object: "list", data },
  };
}
