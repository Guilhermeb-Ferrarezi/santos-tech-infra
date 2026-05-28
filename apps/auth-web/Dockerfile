FROM oven/bun:1 AS builder
WORKDIR /app

COPY package.json ./
RUN bun install

COPY . .

ARG VITE_API_URL
ARG VITE_REDIRECT_DEFAULT
ENV VITE_API_URL=$VITE_API_URL
ENV VITE_REDIRECT_DEFAULT=$VITE_REDIRECT_DEFAULT

RUN bun run build

FROM nginx:alpine
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
