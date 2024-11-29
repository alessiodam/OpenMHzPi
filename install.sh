#!/bin/bash
# Install script for OpenMHzPi


echo "   ____                   __  __ _    _     _____ _ ";
echo "  / __ \                 |  \/  | |  | |   |  __ (_)";
echo " | |  | |_ __   ___ _ __ | \  / | |__| |___| |__) | ";
echo " | |  | | '_ \ / _ \ '_ \| |\/| |  __  |_  /  ___/ |";
echo " | |__| | |_) |  __/ | | | |  | | |  | |/ /| |   | |";
echo "  \____/| .__/ \___|_| |_|_|  |_|_|  |_/___|_|   |_|";
echo "        | |                                         ";
echo "        |_|                                         ";

if ! command -v docker &> /dev/null; then
    read -p "Docker is not installed. Do you want to install it? (y/n): " install_docker
    if [ "$install_docker" != "y" ]; then
        echo "Docker is required to run this script. Exiting."
        exit 1
    fi
    echo "Installing Docker..."
    curl -fsSL https://get.docker.com | sh
fi

if [ "$(docker ps -q -f name=flaresolverr)" ]; then
    echo "FlareSolverr container is already running. Skipping this step."
else
    if lsof -i:8191 &> /dev/null; then
        echo "Port 8191 is already in use. Please free the port and try again."
        exit 1
    fi

    echo "Running FlareSolverr inside Docker..."
    docker run -d --name=flaresolverr -p 8191:8191 -e LOG_LEVEL=info --restart always ghcr.io/flaresolverr/flaresolverr:latest
fi

echo "Downloading the latest binary..."
wget -O /usr/bin/openmhzpi https://github.com/alessiodam/OpenMHzPi/releases/latest/download/openmhzpi-linux-$(uname -m)

echo "Making the binary executable..."
chmod +x /usr/bin/openmhzpi
