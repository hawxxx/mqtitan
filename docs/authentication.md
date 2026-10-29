# Authentication and single sign-on

The controller accepts several credential types at once. A request is allowed when any configured method accepts it:

| Method | Settings | Typical use |
| --- | --- | --- |
| Bearer token | `MQTITAN_TOKEN` | CLI, CI, Prometheus, the Helm chart's default |
| OIDC browser sign-in | `MQTITAN_OIDC_*` with `MQTITAN_OIDC_REDIRECT_URL` | People using the dashboard |
| OIDC bearer tokens | `MQTITAN_OIDC_ISSUER`, `MQTITAN_OIDC_CLIENT_ID` | Automation holding IdP-issued JWTs |
| Basic authentication | `MQTITAN_BASIC_USERNAME`, `MQTITAN_BASIC_PASSWORD` | Small labs |
| Trusted reverse proxy | `MQTITAN_PROXY_IDENTITY_HEADER`, `MQTITAN_TRUSTED_PROXY_CIDRS` | An authenticating proxy in front |

Worker routes are separate: with `MQTITAN_WORKER_TOKEN` set, only that token is accepted there. See [operations](operations.md#security).

## OpenID Connect

MQTiTan is an OIDC relying party. It works with any provider that publishes discovery metadata at `<issuer>/.well-known/openid-configuration` and signs tokens with an asymmetric key (RS256, ES256 and similar), including authentik, Keycloak, Microsoft Entra ID, Okta, Auth0, Dex, Zitadel, Google and GitLab.

Browser sign-in uses the authorization code flow with PKCE (S256), a `state` value and a `nonce`. After the ID token is verified (signature, issuer, audience, expiry, nonce), the controller stores the user's subject, name, e-mail and matching groups in an AES-256-GCM encrypted, `HttpOnly`, `SameSite=Lax` cookie. Over HTTPS the cookie is `Secure` and uses the `__Host-` prefix. Cookie-authenticated requests that change state must come from the controller's own origin.

Bearer JWTs from the same issuer are accepted on the API too, so scripts can use tokens from the identity provider instead of the static `MQTITAN_TOKEN`.

### Settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `MQTITAN_OIDC_ISSUER` | required | Issuer URL. It must match the discovery document's `issuer` exactly, including any trailing slash. HTTPS is required except for loopback hosts. |
| `MQTITAN_OIDC_CLIENT_ID` | required | Client ID. Always accepted as a token audience. |
| `MQTITAN_OIDC_CLIENT_SECRET` | empty | Client secret for confidential clients. Leave empty for a public client; PKCE still protects the code. |
| `MQTITAN_OIDC_REDIRECT_URL` | empty | Public callback URL, `https://<host>/auth/callback`. Setting it enables browser sign-in. |
| `MQTITAN_OIDC_POST_LOGOUT_REDIRECT_URL` | empty | Sent as `post_logout_redirect_uri` on sign-out. Register it at the provider. |
| `MQTITAN_OIDC_SCOPES` | `openid,profile,email` | Requested scopes, comma- or space-separated. `openid` is always added. |
| `MQTITAN_OIDC_AUDIENCES` | empty | Extra audiences accepted on bearer tokens, for access tokens issued to an API or another client. |
| `MQTITAN_OIDC_ALLOWED_GROUPS` | empty | Users must belong to at least one of these groups. Applies to sessions and bearer tokens. Empty means anyone the provider lets sign in to this client. |
| `MQTITAN_OIDC_GROUPS_CLAIM` | `groups` | Claim holding group names. Use `roles` for app roles. |
| `MQTITAN_OIDC_USERNAME_CLAIM` | `preferred_username` | Claim shown as the user's name; falls back to `name`. |
| `MQTITAN_OIDC_SESSION_TTL` | `8h` | Session lifetime (Go duration, at least `1m`). |
| `MQTITAN_OIDC_COOKIE_SECRET` | random | At least 32 characters. Without it, a random key is generated at start and every restart signs everyone out. |
| `MQTITAN_OIDC_CA_FILE` | system roots | PEM bundle added to the system roots, for a provider behind a private CA. |

Discovery runs when the controller starts. If the provider is unreachable or the metadata is wrong, the controller exits with `OIDC discovery failed`.

Sessions are not re-checked with the provider until they expire. Removing a user from an allowed group takes effect at their next sign-in or after `MQTITAN_OIDC_SESSION_TTL`. Shorten the TTL if you need faster revocation.

Signing out clears the session cookie and, when the provider advertises an `end_session_endpoint`, sends the browser there with `client_id` (and `post_logout_redirect_uri` when configured) so the provider session ends as well.

### Endpoints

| Route | Purpose |
| --- | --- |
| `GET /auth/session` | Whether authentication is enabled, whether browser sign-in is available, and the current user. Never requires credentials. |
| `GET /auth/login?next=/path` | Starts sign-in, then returns to `next` (same-origin paths only). |
| `GET /auth/callback` | Redirect URI registered at the provider. |
| `POST /auth/logout` | Clears the session and returns the provider's sign-out URL as `data.redirect`. |

## authentik

1. In the admin interface, open **Applications > Providers** and create an **OAuth2/OpenID Provider**:
   - **Client type**: Confidential (or Public, then leave the client secret unset in MQTiTan).
   - **Redirect URIs**: `strict` `https://mqtitan.example.com/auth/callback`.
   - **Signing Key**: select a certificate, for example the built-in self-signed one. Without a signing key authentik signs with HS256 and the client secret, which MQTiTan does not accept.
   - **Scopes**: keep `openid`, `email` and `profile`. authentik's default `profile` mapping includes a `groups` claim.
2. Create an **Application** that uses this provider, for example with slug `mqtitan`. Bind the groups or users that may sign in under **Policy / Group / User Bindings**.
3. Note the **OpenID Configuration Issuer** shown on the provider page, `https://authentik.example.com/application/o/mqtitan/` (keep the trailing slash).
4. Configure the controller:

```sh
MQTITAN_OIDC_ISSUER=https://authentik.example.com/application/o/mqtitan/
MQTITAN_OIDC_CLIENT_ID=<client ID>
MQTITAN_OIDC_CLIENT_SECRET=<client secret>
MQTITAN_OIDC_REDIRECT_URL=https://mqtitan.example.com/auth/callback
MQTITAN_OIDC_ALLOWED_GROUPS=mqtitan-users        # optional, group names as shown in authentik
MQTITAN_OIDC_COOKIE_SECRET=$(openssl rand -hex 32)
```

For sign-out back to MQTiTan, set `MQTITAN_OIDC_POST_LOGOUT_REDIRECT_URL=https://mqtitan.example.com/` and choose an invalidation flow on the provider that honours it.

Automation can use authentik's `client_credentials` grant with a service account and app password. The resulting JWT has the client ID as its audience, so the controller accepts it as a bearer token. With `MQTITAN_OIDC_ALLOWED_GROUPS` set, add the service account to an allowed group.

```sh
TOKEN=$(curl -s https://authentik.example.com/application/o/token/ \
  -d grant_type=client_credentials -d client_id="$CLIENT_ID" \
  -d username=mqtitan-ci -d password="$APP_PASSWORD" -d scope='openid profile' | jq -r .access_token)
MQTITAN_TOKEN=$TOKEN mqtitan workers --controller https://mqtitan.example.com
```

## Other providers

**Keycloak.** Issuer `https://keycloak.example.com/realms/<realm>`. Create an OpenID Connect client with standard flow and the callback as a valid redirect URI. Keycloak does not emit groups by default: add a **Group Membership** mapper with claim name `groups` and **Full group path** off. For bearer access tokens, add an **Audience** mapper for the client or list the audience in `MQTITAN_OIDC_AUDIENCES`; Keycloak's default access-token audience is `account`. Register the post-logout redirect URI on the client if you set one.

**Microsoft Entra ID.** Issuer `https://login.microsoftonline.com/<tenant-id>/v2.0`. Register a web application with the callback URL. The `groups` claim contains group object IDs, so list IDs in `MQTITAN_OIDC_ALLOWED_GROUPS`, or define app roles and set `MQTITAN_OIDC_GROUPS_CLAIM=roles`. Set `MQTITAN_OIDC_USERNAME_CLAIM=preferred_username` (the default) or `email`. Access tokens for your own API must be v2 tokens (`accessTokenAcceptedVersion: 2`) or their issuer will not match.

**Okta / Auth0.** Use the authorization server's issuer (`https://<org>.okta.com/oauth2/default`, `https://<tenant>.auth0.com/`). Add a groups claim through the authorization server's claims or an Action.

**Google.** Issuer `https://accounts.google.com`. Google does not issue group claims; restrict access through the OAuth consent screen (internal apps) rather than `MQTITAN_OIDC_ALLOWED_GROUPS`.

## Helm

```yaml
ingress:
  enabled: true
  host: mqtitan.example.com
  tlsSecret: mqtitan-tls
auth:
  oidc:
    enabled: true
    issuer: https://authentik.example.com/application/o/mqtitan/
    clientId: <client ID>
    allowedGroups: [mqtitan-users]
    existingSecret: mqtitan-oidc     # keys: client-secret, cookie-secret
    egress:
      - to: [{ipBlock: {cidr: 203.0.113.10/32}}]
        ports: [{protocol: TCP, port: 443}]
```

`redirectUrl` defaults to `https://<ingress.host>/auth/callback`. `caConfigMap` names a ConfigMap with a `ca.crt` key for a private CA. `egress` adds NetworkPolicy rules so the controller can reach the provider; without them discovery fails. The chart still requires the `auth.existingSecret` bearer token, which remains useful for the CLI and the ServiceMonitor.

## Troubleshooting

- `OIDC discovery failed`: the controller cannot fetch `<issuer>/.well-known/openid-configuration`, the TLS chain is not trusted (set `MQTITAN_OIDC_CA_FILE`), or the issuer in the document differs from `MQTITAN_OIDC_ISSUER` (check the trailing slash).
- `ID token verification failed` after sign-in: the token is signed with HS256 (select a signing key in authentik), or the controller's clock is wrong.
- `sign-in expired; start again from the dashboard`: the sign-in took longer than 10 minutes, or the callback reached a different host than the one that started sign-in. The redirect URL host must be the host users browse to.
- `your account is not in a group allowed to use MQTiTan`: the groups claim is missing or does not contain an allowed group. Decode the ID token at the provider to inspect the claim name and values.
- A bearer token is rejected: check its `aud` against the client ID and `MQTITAN_OIDC_AUDIENCES`, and its `iss` against the issuer.
