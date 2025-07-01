# Tạo thư mục ẩn nếu cần
mkdir .config.yaml

# Sao chép file cấu hình
cp config.yaml .config.yaml

# Cài đặt các dependency
go mod tidy

# Chạy ứng dụng
go run ./src/main.go
