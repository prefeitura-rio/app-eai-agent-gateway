#!/usr/bin/env python3
"""
k6 Results Chart Generator
Generates histograms and charts from k6 JSON output
"""

import json
import matplotlib.pyplot as plt
import numpy as np
import pandas as pd
from pathlib import Path
import argparse
import seaborn as sns

def load_k6_results(json_file):
    """Load k6 results from JSON file"""
    # k6 outputs a stream of metric points, not a single aggregated result
    # We need to collect all points and aggregate them ourselves
    
    metrics = {
        'message_completion_time': {'values': []},
        'successful_message_completion_time': {'values': []},
        'failed_message_completion_time': {'values': []},
        'message_success_rate': {'rate': 0.0}
    }
    
    successful_count = 0
    total_count = 0
    
    with open(json_file, 'r') as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            
            try:
                data = json.loads(line)
                
                # Only process Point type data (actual metric values)
                if data.get('type') == 'Point':
                    metric_name = data.get('metric')
                    value = data.get('data', {}).get('value')
                    
                    if metric_name in metrics and value is not None:
                        if metric_name == 'message_completion_time':
                            metrics[metric_name]['values'].append(value)
                            total_count += 1
                        elif metric_name == 'successful_message_completion_time':
                            metrics[metric_name]['values'].append(value)
                            successful_count += 1
                        elif metric_name == 'failed_message_completion_time':
                            metrics[metric_name]['values'].append(value)
                
            except json.JSONDecodeError:
                continue
    
    # Calculate success rate
    if total_count > 0:
        metrics['message_success_rate']['rate'] = successful_count / total_count
    
    # Create a structure similar to what the original code expected
    result = {
        'metrics': metrics,
        'root_group': {
            'checks': {}
        }
    }
    
    return result

def create_histograms(data, output_dir="charts"):
    """Create comprehensive histograms from k6 data"""
    output_path = Path(output_dir)
    output_path.mkdir(exist_ok=True)
    
    # Set style
    plt.style.use('seaborn-v0_8')
    sns.set_palette("husl")
    
    # Extract metrics
    metrics = data.get('metrics', {})
    
    # Create figure with subplots
    fig, axes = plt.subplots(2, 2, figsize=(15, 12))
    fig.suptitle('k6 Load Test Results - Message Completion Time Distribution', fontsize=16, fontweight='bold')
    
    # 1. All Messages Histogram
    if 'message_completion_time' in metrics:
        times = metrics['message_completion_time']['values']
        if len(times) > 0:
            # Use Sturges' rule for optimal bin count: bins = 1 + 3.322 * log10(n)
            bins = max(10, min(50, int(1 + 3.322 * np.log10(len(times)))))
            axes[0, 0].hist(times, bins=bins, alpha=0.7, color='skyblue', edgecolor='black')
            axes[0, 0].set_title('All Messages Completion Time')
            axes[0, 0].set_xlabel('Time (ms)')
            axes[0, 0].set_ylabel('Frequency')
            axes[0, 0].axvline(np.mean(times), color='red', linestyle='--', label=f'Mean: {np.mean(times):.0f}ms')
            axes[0, 0].axvline(np.percentile(times, 95), color='orange', linestyle='--', label=f'P95: {np.percentile(times, 95):.0f}ms')
            axes[0, 0].legend()
        else:
            axes[0, 0].text(0.5, 0.5, 'No message data', transform=axes[0, 0].transAxes, 
                           ha='center', va='center', fontsize=14, color='gray')
            axes[0, 0].set_title('All Messages Completion Time')
        axes[0, 0].grid(True, alpha=0.3)
    
    # 2. Successful Messages Histogram
    if 'successful_message_completion_time' in metrics:
        times = metrics['successful_message_completion_time']['values']
        if len(times) > 0:
            # Use Sturges' rule for optimal bin count: bins = 1 + 3.322 * log10(n)
            bins = max(10, min(50, int(1 + 3.322 * np.log10(len(times)))))
            axes[0, 1].hist(times, bins=bins, alpha=0.7, color='lightgreen', edgecolor='black')
            axes[0, 1].set_title('Successful Messages Completion Time')
            axes[0, 1].set_xlabel('Time (ms)')
            axes[0, 1].set_ylabel('Frequency')
            axes[0, 1].axvline(np.mean(times), color='red', linestyle='--', label=f'Mean: {np.mean(times):.0f}ms')
            axes[0, 1].axvline(np.percentile(times, 95), color='orange', linestyle='--', label=f'P95: {np.percentile(times, 95):.0f}ms')
            axes[0, 1].legend()
        else:
            axes[0, 1].text(0.5, 0.5, 'No successful messages', transform=axes[0, 1].transAxes, 
                           ha='center', va='center', fontsize=14, color='orange')
            axes[0, 1].set_title('Successful Messages Completion Time')
        axes[0, 1].grid(True, alpha=0.3)
    
    # 3. Failed Messages Histogram
    if 'failed_message_completion_time' in metrics:
        times = metrics['failed_message_completion_time']['values']
        if len(times) > 0:
            # Use Sturges' rule for optimal bin count: bins = 1 + 3.322 * log10(n)
            bins = max(10, min(50, int(1 + 3.322 * np.log10(len(times)))))
            axes[1, 0].hist(times, bins=bins, alpha=0.7, color='lightcoral', edgecolor='black')
            axes[1, 0].set_title('Failed Messages Completion Time')
            axes[1, 0].set_xlabel('Time (ms)')
            axes[1, 0].set_ylabel('Frequency')
            axes[1, 0].axvline(np.mean(times), color='red', linestyle='--', label=f'Mean: {np.mean(times):.0f}ms')
            axes[1, 0].axvline(np.percentile(times, 95), color='orange', linestyle='--', label=f'P95: {np.percentile(times, 95):.0f}ms')
            axes[1, 0].legend()
        else:
            axes[1, 0].text(0.5, 0.5, 'No failed messages', transform=axes[1, 0].transAxes, 
                           ha='center', va='center', fontsize=14, color='green')
            axes[1, 0].set_title('Failed Messages Completion Time')
        axes[1, 0].grid(True, alpha=0.3)
    
    # 4. Success Rate Pie Chart
    if 'message_success_rate' in metrics:
        success_rate = metrics['message_success_rate']['rate']
        failure_rate = 1 - success_rate
        
        labels = ['Successful', 'Failed']
        sizes = [success_rate, failure_rate]
        colors = ['lightgreen', 'lightcoral']
        
        axes[1, 1].pie(sizes, labels=labels, colors=colors, autopct='%1.1f%%', startangle=90)
        axes[1, 1].set_title('Message Success Rate')
    
    plt.tight_layout()
    plt.savefig(output_path / 'message_completion_histograms.png', dpi=300, bbox_inches='tight')
    plt.close()
    
    print(f"✅ Histograms saved to {output_path / 'message_completion_histograms.png'}")

def create_detailed_analysis(data, output_dir="charts"):
    """Create detailed analysis charts"""
    output_path = Path(output_dir)
    output_path.mkdir(exist_ok=True)
    
    metrics = data.get('metrics', {})
    
    # Create detailed percentile analysis
    fig, axes = plt.subplots(1, 2, figsize=(15, 6))
    fig.suptitle('Detailed Performance Analysis', fontsize=16, fontweight='bold')
    
    # Percentile comparison
    categories = []
    p50_values = []
    p95_values = []
    p99_values = []
    
    for metric_name in ['message_completion_time', 'successful_message_completion_time', 'failed_message_completion_time']:
        if metric_name in metrics:
            times = metrics[metric_name]['values']
            if len(times) > 0:
                categories.append(metric_name.replace('_', ' ').title())
                p50_values.append(np.percentile(times, 50))
                p95_values.append(np.percentile(times, 95))
                p99_values.append(np.percentile(times, 99))
    
    x = np.arange(len(categories))
    width = 0.25
    
    axes[0].bar(x - width, p50_values, width, label='P50', alpha=0.8)
    axes[0].bar(x, p95_values, width, label='P95', alpha=0.8)
    axes[0].bar(x + width, p99_values, width, label='P99', alpha=0.8)
    
    axes[0].set_xlabel('Message Categories')
    axes[0].set_ylabel('Time (ms)')
    axes[0].set_title('Percentile Comparison')
    axes[0].set_xticks(x)
    axes[0].set_xticklabels(categories, rotation=45)
    axes[0].legend()
    axes[0].grid(True, alpha=0.3)
    
    # Box plot
    all_data = []
    all_labels = []
    
    for metric_name in ['message_completion_time', 'successful_message_completion_time', 'failed_message_completion_time']:
        if metric_name in metrics:
            times = metrics[metric_name]['values']
            if len(times) > 0:
                all_data.append(times)
                all_labels.append(metric_name.replace('_', ' ').title())
    
    if all_data:
        axes[1].boxplot(all_data, tick_labels=all_labels)
        axes[1].set_ylabel('Time (ms)')
        axes[1].set_title('Distribution Box Plot')
        axes[1].grid(True, alpha=0.3)
    
    plt.tight_layout()
    plt.savefig(output_path / 'detailed_analysis.png', dpi=300, bbox_inches='tight')
    plt.close()
    
    print(f"✅ Detailed analysis saved to {output_path / 'detailed_analysis.png'}")

def create_summary_report(data, output_dir="charts"):
    """Create a text summary report"""
    output_path = Path(output_dir)
    output_path.mkdir(exist_ok=True)
    
    metrics = data.get('metrics', {})
    
    report = []
    report.append("=" * 60)
    report.append("k6 LOAD TEST RESULTS SUMMARY")
    report.append("=" * 60)
    report.append("")
    
    # Overall statistics
    if 'message_completion_time' in metrics:
        times = metrics['message_completion_time']['values']
        report.append("ALL MESSAGES:")
        report.append(f"  Count: {len(times)}")
        report.append(f"  Mean: {np.mean(times):.2f}ms")
        report.append(f"  Median: {np.median(times):.2f}ms")
        report.append(f"  P50: {np.percentile(times, 50):.2f}ms")
        report.append(f"  P95: {np.percentile(times, 95):.2f}ms")
        report.append(f"  P99: {np.percentile(times, 99):.2f}ms")
        report.append(f"  Min: {np.min(times):.2f}ms")
        report.append(f"  Max: {np.max(times):.2f}ms")
        report.append("")
    
    if 'successful_message_completion_time' in metrics:
        times = metrics['successful_message_completion_time']['values']
        report.append("SUCCESSFUL MESSAGES:")
        report.append(f"  Count: {len(times)}")
        report.append(f"  Mean: {np.mean(times):.2f}ms")
        report.append(f"  Median: {np.median(times):.2f}ms")
        report.append(f"  P50: {np.percentile(times, 50):.2f}ms")
        report.append(f"  P95: {np.percentile(times, 95):.2f}ms")
        report.append(f"  P99: {np.percentile(times, 99):.2f}ms")
        report.append("")
    
    if 'failed_message_completion_time' in metrics:
        times = metrics['failed_message_completion_time']['values']
        report.append("FAILED MESSAGES:")
        report.append(f"  Count: {len(times)}")
        if len(times) > 0:
            report.append(f"  Mean: {np.mean(times):.2f}ms")
            report.append(f"  Median: {np.median(times):.2f}ms")
            report.append(f"  P50: {np.percentile(times, 50):.2f}ms")
            report.append(f"  P95: {np.percentile(times, 95):.2f}ms")
            report.append(f"  P99: {np.percentile(times, 99):.2f}ms")
        report.append("")
    
    if 'message_success_rate' in metrics:
        success_rate = metrics['message_success_rate']['rate']
        report.append(f"SUCCESS RATE: {success_rate:.2%}")
        report.append("")
    
    # Threshold analysis
    report.append("THRESHOLD ANALYSIS:")
    thresholds = data.get('root_group', {}).get('checks', {})
    for check_name, check_data in thresholds.items():
        if isinstance(check_data, dict) and 'passes' in check_data:
            passes = check_data['passes']
            fails = check_data['fails']
            total = passes + fails
            pass_rate = passes / total if total > 0 else 0
            report.append(f"  {check_name}: {pass_rate:.2%} ({passes}/{total})")
    
    report.append("")
    report.append("=" * 60)
    
    # Save report
    with open(output_path / 'summary_report.txt', 'w') as f:
        f.write('\n'.join(report))
    
    print(f"✅ Summary report saved to {output_path / 'summary_report.txt'}")
    
    # Also print to console
    print('\n'.join(report))

def main():
    parser = argparse.ArgumentParser(description='Generate charts from k6 JSON results')
    parser.add_argument('json_file', help='Path to k6 JSON results file')
    parser.add_argument('--output-dir', default='charts', help='Output directory for charts')
    
    args = parser.parse_args()
    
    if not Path(args.json_file).exists():
        print(f"❌ Error: File {args.json_file} not found")
        return
    
    print(f"📊 Loading k6 results from {args.json_file}...")
    data = load_k6_results(args.json_file)
    
    print("📈 Generating charts...")
    create_histograms(data, args.output_dir)
    create_detailed_analysis(data, args.output_dir)
    create_summary_report(data, args.output_dir)
    
    print(f"\n🎉 All charts and reports generated in '{args.output_dir}' directory!")

if __name__ == "__main__":
    main() 