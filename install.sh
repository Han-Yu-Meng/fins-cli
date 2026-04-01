#!/bin/bash

set -e

GITHUB_USER="Han-Yu-Meng"
GITHUB_REPO="fins-cli"
BRANCH="main"

GH_PROXY="https://ghp.ci/"

# Define color output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# 1. Get the real user
if [ -n "$SUDO_USER" ]; then
    REAL_USER="$SUDO_USER"
else
    REAL_USER="$USER"
fi
REAL_HOME=$(eval echo "~$REAL_USER")

log_info "Current installation target user: $REAL_USER, Directory: $REAL_HOME"

# 2. Check and request sudo privileges
if [ "$EUID" -ne 0 ]; then
  log_warn "This script requires root privileges to install dependencies and system services."
  log_warn "Please run using: sudo ./install.sh, or enter password to continue:"
  sudo -v
fi

# 3. Install system dependencies
log_info "Installing system dependencies"
sudo apt-get update -y
sudo apt-get install -y ninja-build build-essential curl jq wget
log_success "System dependencies installed successfully."

# 4. Get the latest version of binary files from GitHub
log_info "Querying the Release version from GitHub..."

# Try to get the latest stable release, fallback to include pre-releases (for 'latest' tag)
API_URL="https://api.github.com/repos/$GITHUB_USER/$GITHUB_REPO/releases/latest"
# Use -s to be silent, but check for rate limits or other issues
LATEST_RELEASE=$(curl -s "$API_URL")

# Improved check: If stable is missing, has no assets, OR rate limited, try the 'latest' tag
RATE_LIMITED=$(echo "$LATEST_RELEASE" | grep -q "rate limit exceeded" && echo "true" || echo "false")
HAS_ASSETS=$(echo "$LATEST_RELEASE" | jq -r 'if .assets then (.assets | length > 0) else false end' 2>/dev/null || echo "false")

if [ "$RATE_LIMITED" = "true" ] || echo "$LATEST_RELEASE" | grep -q "Not Found" || [ "$HAS_ASSETS" != "true" ]; then
    log_warn "API rate limited or latest stable release not found. Attempting to use static 'latest' tag URLs..."
    
    # Define fallback URLs directly based on your naming convention in release.yml
    FINS_URL="${GH_PROXY}https://github.com/$GITHUB_USER/$GITHUB_REPO/releases/download/latest/fins-linux-amd64"
    FINSD_URL="${GH_PROXY}https://github.com/$GITHUB_USER/$GITHUB_REPO/releases/download/latest/finsd-linux-amd64"
    
    log_info "Attempting direct download from: $FINS_URL"
else
    # Parse download links
    FINS_URL=$(echo "$LATEST_RELEASE" | jq -r '.assets[]? | select(.name | test("fins-linux-amd64|fins$")) | .browser_download_url' | head -n 1)
    FINSD_URL=$(echo "$LATEST_RELEASE" | jq -r '.assets[]? | select(.name | test("finsd-linux-amd64|finsd$")) | .browser_download_url' | head -n 1)

    # Prefix with proxy
    FINS_URL="${GH_PROXY}${FINS_URL}"
    FINSD_URL="${GH_PROXY}${FINSD_URL}"
fi

if [ -z "$FINS_URL" ] || [ -z "$FINSD_URL" ]; then
    log_error "Could not determine download URLs."
    exit 1
fi

log_info "Downloading fins : $FINS_URL"
sudo curl -sL "$FINS_URL" -o /usr/local/bin/fins
log_info "Downloading finsd: $FINSD_URL"
sudo curl -sL "$FINSD_URL" -o /usr/local/bin/finsd

# Grant execution permissions
sudo chmod +x /usr/local/bin/fins
sudo chmod +x /usr/local/bin/finsd
log_success "Binary files downloaded and installed successfully."

# 5. Download default configuration files to user directory ~/.fins/
FINS_DIR="$REAL_HOME/.fins"
log_info "Configuring default files to $FINS_DIR ..."

sudo -u "$REAL_USER" mkdir -p "$FINS_DIR"
sudo -u "$REAL_USER" mkdir -p "$FINS_DIR/logs"

CONFIG_URL="${GH_PROXY}https://raw.githubusercontent.com/$GITHUB_USER/$GITHUB_REPO/$BRANCH/default/config.yaml"
RECIPE_URL="${GH_PROXY}https://raw.githubusercontent.com/$GITHUB_USER/$GITHUB_REPO/$BRANCH/default/recipes.yaml"

sudo -u "$REAL_USER" curl -sL "$CONFIG_URL" -o "$FINS_DIR/config.yaml"
sudo -u "$REAL_USER" curl -sL "$RECIPE_URL" -o "$FINS_DIR/recipes.yaml"

log_success "Configuration files download complete."

# 6. Configure systemd background service
log_info "Configuring finsd systemd service to start on boot..."

# Check if systemd is available
if ! systemctl status --no-pager &>/dev/null && [ "$?" -ne 0 ]; then
    log_warn "systemd is not available (common in WSL without systemd enabled)."
    log_warn "Skipping systemd service configuration."
    log_info "You can start the daemon manually by running: finsd &"
    HAS_SYSTEMD="false"
else
    SYSTEMD_FILE="/etc/systemd/system/finsd.service"

    sudo bash -c "cat > $SYSTEMD_FILE" <<EOF
[Unit]
Description=FINS Daemon Service
After=network.target

[Service]
Type=simple
User=$REAL_USER
Group=$REAL_USER
WorkingDirectory=$REAL_HOME
ExecStart=/usr/local/bin/finsd
Restart=always
RestartSec=3
Environment="HOME=$REAL_HOME"
Environment="USER=$REAL_USER"
Environment="PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

[Install]
WantedBy=multi-user.target
EOF

    # Reload systemd and start the service
    sudo systemctl daemon-reload
    sudo systemctl enable finsd
    sudo systemctl restart finsd
    log_success "finsd service configured and started."
    HAS_SYSTEMD="true"
fi

# 7. Final Tips
echo ""
echo -e "${GREEN}======================================================================${NC}"
echo -e "${GREEN}  🎉 FINS Installation Complete!${NC}"
echo -e "${GREEN}======================================================================${NC}"
echo ""

if [ "$HAS_SYSTEMD" = "true" ]; then
    echo -e "The background daemon ${YELLOW}finsd${NC} is running. You can check its status with:"
    echo -e "  ${BLUE}systemctl status finsd${NC}"
else
    echo -e "${YELLOW}[Notice]${NC} systemd not found. Please start the daemon manually:"
    echo -e "  ${BLUE}nohup finsd > /dev/null 2>&1 &${NC}"
fi
echo ""
echo -e "${RED}[Important Next Steps]${NC}"
echo -e "To use Agent and Inspect features correctly, please run the following commands to compile internal tools:"
echo ""
echo -e "  ${YELLOW}fins agent build${NC}"
echo -e "  ${YELLOW}fins inspect build${NC}"
echo ""
echo -e "You can use ${YELLOW}fins --help${NC} anytime to view the help documentation."
echo ""