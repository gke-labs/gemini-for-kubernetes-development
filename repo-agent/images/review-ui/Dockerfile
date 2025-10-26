# Build stage
FROM node:18-alpine AS builder
WORKDIR /app
COPY review-ui/ui/package.json ./
RUN npm install
COPY review-ui/ui/ .
RUN npm run build

# Final stage
FROM nginx:1.25-alpine
COPY --from=builder /app/build /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
