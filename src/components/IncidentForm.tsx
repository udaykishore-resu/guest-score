import { useState } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { useToast } from "@/components/ui/use-toast";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

interface IncidentFormProps {
  onScoreUpdate: (newScore: number) => void;
  currentScore: number;
}

export const IncidentForm = ({ onScoreUpdate, currentScore }: IncidentFormProps) => {
  const { toast } = useToast();
  const [loading, setLoading] = useState(false);
  const [severity, setSeverity] = useState<string>("minor");

  const calculateScoreImpact = (severity: string): number => {
    switch (severity) {
      case "minor":
        return -5;
      case "moderate":
        return -10;
      case "major":
        return -20;
      default:
        return 0;
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLoading(true);
    
    // Calculate new score
    const scoreImpact = calculateScoreImpact(severity);
    const newScore = Math.max(0, Math.min(100, currentScore + scoreImpact));
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 1000));
    
    // Update score and show toast
    onScoreUpdate(newScore);
    toast({
      title: "Incident Reported",
      description: `The incident has been recorded. Guest score updated by ${scoreImpact} points.`,
    });
    
    setLoading(false);
    setSeverity("minor");
    (e.target as HTMLFormElement).reset();
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Report Incident</CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <label htmlFor="type" className="text-sm font-medium text-muted-foreground">
              Incident Type
            </label>
            <Input
              id="type"
              placeholder="e.g., Property Damage, Policy Violation"
              required
            />
          </div>
          <div>
            <label htmlFor="severity" className="text-sm font-medium text-muted-foreground">
              Severity Level
            </label>
            <Select
              value={severity}
              onValueChange={setSeverity}
            >
              <SelectTrigger>
                <SelectValue placeholder="Select severity" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="minor">Minor (-5 points)</SelectItem>
                <SelectItem value="moderate">Moderate (-10 points)</SelectItem>
                <SelectItem value="major">Major (-20 points)</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div>
            <label htmlFor="description" className="text-sm font-medium text-muted-foreground">
              Description
            </label>
            <Textarea
              id="description"
              placeholder="Describe the incident in detail..."
              className="min-h-[100px]"
              required
            />
          </div>
          <Button type="submit" className="w-full" disabled={loading}>
            {loading ? "Submitting..." : "Submit Report"}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
};