FROM public.ecr.aws/docker/library/node:22-alpine
# System Chromium avoids downloading all of Playwright's browser distributions.
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache chromium ca-certificates
RUN apk add --no-cache font-wqy-zenhei
ENV NODE_ENV=production CHROMIUM_PATH=/usr/bin/chromium
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts --registry=https://registry.npmmirror.com && npm cache clean --force
COPY server.mjs web.mjs sessions.mjs seccomp_profile.json PLAYWRIGHT_LICENSE ./
USER node
EXPOSE 8080
CMD ["node", "server.mjs"]
