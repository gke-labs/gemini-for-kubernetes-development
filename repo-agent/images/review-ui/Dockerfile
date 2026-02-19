# Build stage
FROM node:18-alpine AS builder
WORKDIR /app
COPY repo-agent/review-ui/ui/package.json ./
RUN npm install
COPY repo-agent/review-ui/ui/ .
ARG VERSION=unknown
RUN REACT_APP_GIT_SHA=$VERSION npm run build

# Final stage
FROM nginx:1.25-alpine
COPY --from=builder /app/build /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
