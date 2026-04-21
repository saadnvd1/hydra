#!/bin/bash
# Fake AI CLI that simulates hitting a rate limit after some output
echo "Starting fake AI session..."
echo "Processing your request..."
sleep 2
echo "Generating response..."
sleep 1
echo ""
echo "Error: rate limit exceeded. Please try again later."
exit 1
