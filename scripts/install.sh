#!/bin/bash

# Antimage Installation Script
# Usage: curl -fsSL https://panel.example.com/install.sh | bash

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}╔═══════════════════════════════════════╗${NC}"
echo -e "${GREEN}║   Antimage VPN Control Plane Setup   ║${NC}"
echo -e "${GREEN}╚═══════════════════════════════════════╝${NC}"
echo ""

# Detect OS
if [[ "$OSTYPE" == "linux-gnu"* ]]; then
    OS="linux"
elif [[ "$OSTYPE" == "darwin"* ]]; then
    OS="darwin"
else
    echo -e "${RED}Unsupported OS: $OSTYPE${NC}"
    exit 1
fi

# Detect architecture
ARCH=$(uname -m)
case $ARCH in
    x86_64)
        ARCH="amd64"
        ;;
    aarch64|arm64)
        ARCH="arm64"
        ;;
    *)
        echo -e "${RED}Unsupported architecture: $ARCH${NC}"
        exit 1
        ;;
esac

echo -e "${YELLOW}Detected: $OS-$ARCH${NC}"

# Check for required commands
for cmd in curl wget docker docker-compose; do
    if ! command -v $cmd &> /dev/null; then
        echo -e "${YELLOW}Warning: $cmd not found. Some features may not work.${NC}"
    fi
done

# Create installation directory
INSTALL_DIR="/opt/antimage"
echo -e "${YELLOW}Creating installation directory: $INSTALL_DIR${NC}"
sudo mkdir -p $INSTALL_DIR
cd $INSTALL_DIR

# Download docker-compose.yml
echo -e "${YELLOW}Downloading docker-compose.yml...${NC}"
sudo curl -fsSL https://raw.githubusercontent.com/amyrm/antimage/main/docker-compose.yml -o docker-compose.yml

# Download .env.example
echo -e "${YELLOW}Downloading environment template...${NC}"
sudo curl -fsSL https://raw.githubusercontent.com/amyrm/antimage/main/.env.example -o .env

# Generate secret key
echo -e "${YELLOW}Generating secret key...${NC}"
sudo mkdir -p config
sudo openssl rand -base64 32 > config/secret.key

# Create data directories
sudo mkdir -p data node-data node-config grafana-dashboards

# Set permissions
sudo chmod 600 config/secret.key

echo ""
echo -e "${GREEN}Installation files ready!${NC}"
echo ""
echo -e "${YELLOW}Next steps:${NC}"
echo "1. Edit .env with your configuration:"
echo "   sudo nano .env"
echo ""
echo "2. Start the services:"
echo "   sudo docker-compose up -d"
echo ""
echo "3. Create the first admin user:"
echo "   sudo docker-compose exec panel antimage-ctl create-admin \\"
echo "     --username admin --password <your-password> --role super_admin"
echo ""
echo "4. Access the panel at: http://localhost:8080"
echo ""
echo -e "${GREEN}Installation complete!${NC}"
