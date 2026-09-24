# The tag and digest live here only; internal/tools/idppin reads them for every other consumer.
# ghcr.io, not Docker Hub: the Railway builder cannot reach registry-1.docker.io.
FROM ghcr.io/supabase/auth:v2.197.0@sha256:1736a63078f5922b198c4cbe50f80ab9a2d3b54fe8b7b6cfb2e9dc5dbbc12c6b

# Environment-independent settings. Per-environment values and secrets are Railway variables.
ENV GOTRUE_DB_DRIVER=postgres \
    GOTRUE_DB_MAX_POOL_SIZE=10 \
    GOTRUE_JWT_AUD=authenticated \
    GOTRUE_JWT_DEFAULT_GROUP_NAME=authenticated \
    GOTRUE_JWT_EXP=3600 \
    GOTRUE_DISABLE_SIGNUP=true \
    GOTRUE_MAILER_AUTOCONFIRM=false \
    GOTRUE_HOOK_CUSTOM_ACCESS_TOKEN_ENABLED=true \
    GOTRUE_HOOK_CUSTOM_ACCESS_TOKEN_URI=pg-functions://postgres/public/custom_access_token_hook \
    GOTRUE_SMTP_HOST=smtp.resend.com \
    GOTRUE_SMTP_PORT=465 \
    GOTRUE_SMTP_USER=resend \
    GOTRUE_SMTP_ADMIN_EMAIL=no-reply@ascomply.com \
    GOTRUE_SMTP_SENDER_NAME=ASComply \
    GOTRUE_LOG_LEVEL=info
